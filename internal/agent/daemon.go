// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/podmin-dev/podmin/internal/agent/api"
	"github.com/podmin-dev/podmin/internal/agent/coordinator"
	"github.com/podmin-dev/podmin/internal/agent/dataplane"
	"github.com/podmin-dev/podmin/internal/agent/identity"
	"github.com/podmin-dev/podmin/internal/agent/pods"
	"github.com/podmin-dev/podmin/internal/agent/service"
	"github.com/podmin-dev/podmin/internal/agent/staticpod"
	"github.com/podmin-dev/podmin/internal/agent/workload"
	"github.com/podmin-dev/podmin/internal/cloud/aws"
	"github.com/podplane/registry/pkg/registry"
	"github.com/podplane/s3lect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	podsv1alpha1 "k8s.io/kubelet/pkg/apis/pods/v1alpha1"
)

const maxAgentObjectSize = 16 << 20

// DaemonConfig contains provider and cluster settings for one agent process.
type DaemonConfig struct {
	Provider, Bucket, Region, Cluster, NodeGroup string
	IPv6Prefix                                   netip.Prefix
	Logger                                       *slog.Logger
}

// component is one cancellable daemon workload.
type component struct {
	name string
	run  func(context.Context) error
	stop func(context.Context) error
}

// componentResult records one component's termination.
type componentResult struct {
	name string
	err  error
}

// RunDaemon constructs and runs all agent components until cancellation or a fatal error.
func RunDaemon(ctx context.Context, options DaemonConfig) error {
	if options.Provider != "aws" || options.Bucket == "" || options.Region == "" || options.Cluster == "" || options.NodeGroup == "" || !options.IPv6Prefix.IsValid() || !options.IPv6Prefix.Addr().Is6() || options.IPv6Prefix.Addr().Is4In6() || !options.IPv6Prefix.Addr().IsGlobalUnicast() || options.IPv6Prefix.Bits() != 80 || options.IPv6Prefix != options.IPv6Prefix.Masked() {
		return errors.New("invalid required configuration")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	address, err := globalIPv6(options.IPv6Prefix)
	if err != nil {
		return fmt.Errorf("find node address: %w", err)
	}
	advertise := "[" + address.String() + "]:8081"
	provider, err := aws.Load(ctx, options.Region, "")
	if err != nil {
		return fmt.Errorf("configure AWS services: %w", err)
	}
	nodeID, err := provider.InstanceID(ctx)
	if err != nil {
		return err
	}
	objects := provider.ObjectStore(options.Bucket, maxAgentObjectSize)
	registryHandler, err := registry.New(provider.RegistryStore(options.Bucket))
	if err != nil {
		return fmt.Errorf("configure image registry: %w", err)
	}
	parameters := provider.ParameterStore()
	secrets := provider.Secrets()
	clusterCASecret, err := parameters.Get(ctx, "/"+options.Cluster+identity.ClusterCAPathSuffix)
	if err != nil {
		return fmt.Errorf("load cluster CA: %w", err)
	}
	clusterCA, err := identity.Load(clusterCASecret, options.Cluster)
	if err != nil {
		return fmt.Errorf("validate cluster CA: %w", err)
	}
	clientTLS, serverTLS, err := clusterCA.TLSConfigs(options.Cluster, nodeID, options.NodeGroup, address)
	if err != nil {
		return fmt.Errorf("configure coordination mTLS: %w", err)
	}
	key, err := parameters.Get(ctx, "/"+options.Cluster+"/_system/workload-ca-key")
	if err != nil {
		return fmt.Errorf("load workload CA key: %w", err)
	}
	key, err = workload.DecodeKey(key)
	if err != nil {
		return fmt.Errorf("decode workload CA key: %w", err)
	}
	authority, err := workload.New(options.Cluster, key, objects)
	if err != nil {
		return fmt.Errorf("configure workload identity: %w", err)
	}
	elector, err := s3lect.NewS3Elector(s3lect.S3ElectorOptions{Config: &s3lect.ElectorConfig{LockfilePath: "dns/leader.json", ServerID: nodeID, ServerAddr: advertise, FrequentInterval: 5 * time.Second, InfrequentInterval: 30 * time.Second, LeaderTimeout: 15 * time.Second}, Storage: objects, Logger: logger})
	if err != nil {
		return fmt.Errorf("configure DNS election: %w", err)
	}
	dnsHandler := service.NewServer()
	plane := dataplane.New(options.IPv6Prefix)
	controller := service.NewController(options.Cluster, options.NodeGroup, address, dnsHandler, plane)
	reconciler, err := staticpod.NewReconciler(staticpod.Config{Cluster: options.Cluster, NodeGroup: options.NodeGroup, StaticDir: "/etc/podmin/manifests", SecretDir: "/run/podmin", NodeDNS: address.String(), PollInterval: 5 * time.Second, PublishServices: controller.SetServices, Identity: authority}, objects, parameters, secrets)
	if err != nil {
		return fmt.Errorf("configure agent: %w", err)
	}
	coord, err := coordinator.New(coordinator.Config{NodeID: nodeID, Cluster: options.Cluster, NodeGroup: options.NodeGroup, IPv6Prefix: options.IPv6Prefix, Elector: elector, Storage: objects, Controller: controller, Logger: logger, ClientTLS: clientTLS})
	if err != nil {
		return fmt.Errorf("configure DNS coordinator: %w", err)
	}
	if err = coord.LoadSnapshot(ctx); err != nil {
		logger.Warn("load DNS snapshot", "error", err)
	}
	health := &http.Server{Addr: "127.0.0.1:8080", Handler: api.NewHealthHandler(func() bool { return reconciler.Healthy() && controller.Healthy() }), ReadHeaderTimeout: 5 * time.Second}
	registryServer := &http.Server{Addr: "127.0.0.1:5000", Handler: registryHandler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	coordination := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	api.RegisterCoordinationServer(coordination, &api.Server{Backend: coord})
	udp := &dns.Server{Addr: "127.0.0.1:1053", Net: "udp", Handler: dnsHandler}
	tcp := &dns.Server{Addr: "127.0.0.1:1053", Net: "tcp", Handler: dnsHandler}
	components := []component{
		httpComponent("health server", health), httpComponent("image registry", registryServer), grpcComponent("service coordination server", "["+address.String()+"]:8081", coordination),
		dnsComponent("DNS UDP server", udp), dnsComponent("DNS TCP server", tcp),
		{name: "static Pod reconciler", run: func(run context.Context) error { reconciler.Run(run, logger); return nil }, stop: func(context.Context) error { return plane.Close() }},
		{name: "DNS coordinator", run: coord.Run},
		{name: "workload identity", run: func(run context.Context) error { return runWorkloadIdentity(run, authority, elector, logger) }},
		{name: "kubelet PodsAPI controller", run: func(ctx context.Context) error {
			return runPodsControllerWithWatcher(ctx, controller, plane.TriggerRefresh, runPodsWatcher)
		}},
	}
	return runComponents(ctx, components, 6*time.Second)
}

// runWorkloadIdentity keeps public workload CA state synchronized and lets only the elected leader rotate it.
func runWorkloadIdentity(ctx context.Context, authority *workload.Authority, elector s3lect.Elector, logger *slog.Logger) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		now := time.Now()
		if elector.IsLeader() {
			if err := authority.Ensure(ctx, now); err != nil {
				logger.Warn("ensure workload CA", "error", err)
			}
		}
		if err := authority.Sync(ctx, now); err != nil && !errors.Is(err, s3lect.ErrStorageNotFound) {
			logger.Warn("synchronize workload CA", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// runPodsWatcher connects to PodsAPI and publishes complete Pod snapshots until cancellation.
func runPodsWatcher(ctx context.Context, publish func(pods.Snapshot)) {
	connection, err := pods.Dial(pods.DefaultSocket)
	if err != nil {
		publish(pods.Snapshot{})
		return
	}
	defer func() { _ = connection.Close() }()
	watcher := pods.Watcher{Client: pods.GRPCClient{Client: podsv1alpha1.NewPodsClient(connection)}, Publish: publish}
	_ = watcher.Run(ctx)
}

// runPodsControllerWithWatcher publishes Pod state and uses each event as a dataplane refresh hint.
func runPodsControllerWithWatcher(ctx context.Context, controller *service.Controller, refresh func(), watch func(context.Context, func(pods.Snapshot))) error {
	watch(ctx, func(snapshot pods.Snapshot) {
		controller.SetPods(snapshot)
		refresh()
	})
	return nil
}

// grpcComponent adapts a gRPC server with bounded graceful shutdown.
func grpcComponent(name, address string, server *grpc.Server) component {
	var listener net.Listener
	return component{name: name, run: func(context.Context) error {
		var err error
		listener, err = net.Listen("tcp", address)
		if err != nil {
			return err
		}
		return server.Serve(listener)
	}, stop: func(ctx context.Context) error {
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			server.Stop()
		}
		return nil
	}}
}

// runComponents propagates the first fatal error, cancels siblings, and waits for shutdown.
func runComponents(ctx context.Context, components []component, gracefulDelay time.Duration) error {
	runCtx, cancel := context.WithCancel(ctx)
	results := make(chan componentResult, len(components))
	var group sync.WaitGroup
	for _, current := range components {
		group.Add(1)
		go func() { defer group.Done(); results <- componentResult{name: current.name, err: current.run(runCtx)} }()
	}
	var fatal error
	waiting := true
	for waiting {
		select {
		case <-ctx.Done():
			cancel()
			waiting = false
		case result := <-results:
			if ctx.Err() != nil {
				cancel()
				waiting = false
				continue
			}
			fatal = result.err
			if fatal == nil {
				fatal = fmt.Errorf("%s stopped unexpectedly", result.name)
			}
			cancel()
			waiting = false
		}
	}
	cancel()
	if fatal == nil && gracefulDelay > 0 {
		time.Sleep(gracefulDelay)
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	for _, current := range components {
		if current.stop != nil {
			_ = current.stop(shutdown)
		}
	}
	group.Wait()
	return fatal
}

// httpComponent adapts an HTTP server to daemon lifecycle hooks.
func httpComponent(name string, server *http.Server) component {
	return component{name: name, run: func(context.Context) error {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("%s: %w", name, err)
	}, stop: server.Shutdown}
}

// dnsComponent adapts a DNS server to daemon lifecycle hooks.
func dnsComponent(name string, server *dns.Server) component {
	return component{name: name, run: func(context.Context) error {
		err := server.ListenAndServe()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}, stop: server.ShutdownContext}
}

// globalIPv6 returns the node's one global unicast IPv6 address outside its delegated Pod prefix.
func globalIPv6(podPrefix netip.Prefix) (netip.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return netip.Addr{}, err
	}
	var result netip.Addr
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			return netip.Addr{}, addressErr
		}
		for _, value := range addresses {
			prefix, parseErr := netip.ParsePrefix(value.String())
			if parseErr == nil && prefix.Addr().Is6() && !prefix.Addr().Is4In6() && prefix.Addr().IsGlobalUnicast() && !podPrefix.Contains(prefix.Addr()) {
				if result.IsValid() && result != prefix.Addr() {
					return netip.Addr{}, errors.New("multiple global IPv6 addresses")
				}
				result = prefix.Addr()
			}
		}
	}
	if !result.IsValid() {
		return netip.Addr{}, errors.New("no global IPv6 address")
	}
	return result, nil
}
