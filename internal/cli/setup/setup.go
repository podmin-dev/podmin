// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/identity"
	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/podmin-dev/podmin/internal/cli/userdata"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/registry"
	"github.com/podmin-dev/podmin/internal/secrets"
)

const pauseVersion = "3.10.2"
const pauseImage = "registry.k8s.io/pause:" + pauseVersion

var s3BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var s3KeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+=,@-]*$`)

// Options contains user-selected setup inputs and command streams.
type Options struct {
	Context           config.Context
	VPCCIDR           string
	NodeGroups        []string
	AgentSource       string
	NAT64             string
	OTelLogs          string
	WorkloadCAPublish string
	AutoApprove       bool
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
}

// Run creates or updates a cluster from the authoritative setup options.
func Run(ctx context.Context, client *cloud.Client, options Options) error {
	prefix, err := netip.ParsePrefix(options.VPCCIDR)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
		return errors.New("--vpc-cidr must be a private IPv4 CIDR")
	}
	nodeGroups, err := parseNodeGroups(options.NodeGroups)
	if err != nil {
		return err
	}
	otelLogs, err := parseOTelLogs(options.OTelLogs, options.Context)
	if err != nil {
		return err
	}
	if err = verifyOTelLogsSecret(ctx, client, options.Context, otelLogs); err != nil {
		return err
	}
	workloadCAPublication, err := parseWorkloadCAPublication(options.WorkloadCAPublish, options.Context.Bucket)
	if err != nil {
		return err
	}
	progress := func(message string) error {
		_, writeErr := fmt.Fprintln(options.Stdout, message)
		return writeErr
	}
	if err = progress("Inspecting node groups..."); err != nil {
		return err
	}
	requestedZones := make(map[string]string, len(nodeGroups))
	for name, nodeGroup := range nodeGroups {
		requestedZones[name] = nodeGroup.Zone
	}
	nat64, err := parseNAT64(options.NAT64, nodeGroups)
	if err != nil {
		return err
	}
	network, err := client.Compute.Network(ctx, options.Context.ClusterID, prefix.Masked(), requestedZones, nat64 != nil)
	if err != nil {
		return err
	}
	for name, nodeGroup := range nodeGroups {
		nodeGroup.Zone = network.NodeGroupZones[name]
		nodeGroups[name] = nodeGroup
	}
	images, err := resolveCompute(ctx, client.Compute, nodeGroups, nat64)
	if err != nil {
		return err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	if err = progress("Preparing bootstrap downloads..."); err != nil {
		return err
	}
	artifacts, desired, err := syncDependencies(ctx, client.Objects, nodeGroups, nat64, cache, options.AgentSource, options.Stdout, progress)
	if err != nil {
		return err
	}
	if err = addUserData(options.Context, nodeGroups, artifacts, otelLogs, workloadCAPublication); err != nil {
		return err
	}
	if err = progress("Ensuring cluster and workload CAs exist..."); err != nil {
		return err
	}
	if err = ensureCertificateAuthorities(ctx, client.SystemSecrets, options.Context.ClusterID); err != nil {
		return err
	}
	var otelLogsCA *infra.S3Object
	if otelLogs != nil && otelLogs.CA != "" {
		object, parseErr := parseS3Object(otelLogs.CA, "--otel-logs ca")
		if parseErr != nil {
			return parseErr
		}
		otelLogsCA = &object
	}
	variables := infra.Variables{ClusterID: options.Context.ClusterID, Region: options.Context.Region, Profile: options.Context.Profile, Bucket: options.Context.Bucket, WorkloadCAPublication: workloadCAPublication, OTelLogsCA: otelLogsCA, VPCCIDR: prefix.Masked().String(), ManageVPC: network.ManageVPC, NAT64: nat64, SubnetCIDRs: network.NodeGroupCIDRs, NAT64CIDRs: network.NAT64CIDRs, NAT64IPv6: network.NAT64IPv6CIDRs, Images: images, NodeGroups: nodeGroups}
	infrastructure, err := json.MarshalIndent(variables, "", "  ")
	if err != nil {
		return err
	}
	if err = progress("Saving infrastructure configuration..."); err != nil {
		return err
	}
	if err = client.Objects.PutStream(ctx, "tfstate/podmin.auto.tfvars.json", bytes.NewReader(infrastructure), int64(len(infrastructure))); err != nil {
		return err
	}
	if err = progress("Applying infrastructure..."); err != nil {
		return err
	}
	if err = infra.Run(ctx, variables, false, options.AutoApprove, options.Stdin, options.Stdout, options.Stderr); err != nil {
		return fmt.Errorf("%w; infrastructure may have been partially updated, correct the error and rerun podmin setup", err)
	}
	if nat64 != nil {
		groups := make(map[string]bool)
		for name, nodeGroup := range nodeGroups {
			if nodeGroup.NAT64InstanceType != "" {
				groups["nodegroup-"+name] = true
			} else {
				groups["shared-"+nodeGroup.Zone] = true
			}
		}
		names := make([]string, 0, len(groups))
		for group := range groups {
			names = append(names, group)
		}
		sort.Strings(names)
		generation, parseErr := time.Parse(time.RFC3339Nano, nat64.Generation)
		if parseErr != nil {
			return parseErr
		}
		started := time.Now()
		ticker := time.NewTicker(time.Second)
		waitCtx, cancelWait := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- client.Compute.WaitNAT64(waitCtx, options.Context.ClusterID, names, generation)
		}()
		if err = progress("Waiting for NAT64 instances..."); err != nil {
			ticker.Stop()
			cancelWait()
			return err
		}
		for waiting := true; waiting; {
			select {
			case err = <-done:
				waiting = false
			case <-ticker.C:
				err = progress(fmt.Sprintf("Waiting for NAT64 instances... %s elapsed", time.Since(started).Truncate(time.Second)))
			}
			if err != nil {
				ticker.Stop()
				cancelWait()
				return err
			}
		}
		ticker.Stop()
		cancelWait()
		if err = progress(fmt.Sprintf("NAT64 instances ready after %s.", time.Since(started).Round(time.Second))); err != nil {
			return err
		}
	}
	if err = cleanupDependencies(ctx, client.Objects, desired); err != nil {
		_, _ = fmt.Fprintf(options.Stderr, "warning: dependency cleanup failed: %v\n", err)
	}
	return progress("Setup complete.")
}

// parseNodeGroups validates the authoritative NodeGroup definitions.
func parseNodeGroups(values []string) (map[string]infra.NodeGroup, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one --nodegroup is required")
	}
	if len(values) > 256 {
		return nil, errors.New("at most 256 NodeGroups fit in an Amazon-provided IPv6 /56")
	}
	nodeGroups := make(map[string]infra.NodeGroup, len(values))
	for _, value := range values {
		name, nodeGroup, err := infra.ParseNodeGroup(value)
		if err != nil {
			return nil, err
		}
		if !manifest.ValidID(name) {
			return nil, fmt.Errorf("invalid NodeGroup ID %q", name)
		}
		if _, exists := nodeGroups[name]; exists {
			return nil, fmt.Errorf("duplicate NodeGroup %q", name)
		}
		nodeGroups[name] = nodeGroup
	}
	return nodeGroups, nil
}

// resolveCompute records instance architectures and resolves one immutable image per architecture.
func resolveCompute(ctx context.Context, compute cloud.Compute, nodeGroups map[string]infra.NodeGroup, nat64 *infra.NAT64) (map[string]infra.Image, error) {
	images := map[string]cloud.Image{}
	image := func(architecture string) (cloud.Image, error) {
		if existing, ok := images[architecture]; ok {
			return existing, nil
		}
		resolved, err := compute.Image(ctx, architecture)
		if err == nil {
			images[architecture] = resolved
		}
		return resolved, err
	}
	for name, nodeGroup := range nodeGroups {
		architecture, err := compute.Architecture(ctx, nodeGroup.InstanceType)
		if err != nil {
			return nil, err
		}
		nodeGroup.Architecture = architecture
		if _, err = image(architecture); err != nil {
			return nil, err
		}
		if nodeGroup.NAT64InstanceType != "" {
			nodeGroup.NAT64Architecture, err = compute.Architecture(ctx, nodeGroup.NAT64InstanceType)
			if err != nil {
				return nil, err
			}
			machine, err := image(nodeGroup.NAT64Architecture)
			if err != nil {
				return nil, err
			}
			nodeGroup.NAT64Kernel = machine.Kernel
		}
		nodeGroups[name] = nodeGroup
	}
	if nat64 != nil {
		architecture, err := compute.Architecture(ctx, nat64.InstanceType)
		if err != nil {
			return nil, err
		}
		nat64.Architecture = architecture
		machine, err := image(architecture)
		if err != nil {
			return nil, err
		}
		nat64.Kernel = machine.Kernel
	}
	result := make(map[string]infra.Image, len(images))
	for architecture, image := range images {
		result[architecture] = infra.Image{ID: image.ID, RootDeviceName: image.RootDeviceName}
	}
	return result, nil
}

// parseNAT64 validates shared and dedicated NAT64 instance configuration.
func parseNAT64(value string, nodeGroups map[string]infra.NodeGroup) (*infra.NAT64, error) {
	for _, nodeGroup := range nodeGroups {
		if value == "" && nodeGroup.NAT64InstanceType != "" {
			return nil, errors.New("NodeGroup nat64 requires --nat64")
		}
	}
	if value == "" {
		return nil, nil
	}
	nat64, err := infra.ParseNAT64(value)
	if err != nil {
		return nil, err
	}
	nat64.Generation = time.Now().UTC().Format(time.RFC3339Nano)
	return &nat64, nil
}

// parseOTelLogs validates optional OTLP log export configuration.
func parseOTelLogs(value string, selected config.Context) (*userdata.OTelLogs, error) {
	if value == "" {
		return nil, nil
	}
	settings := make(map[string]string)
	seen := make(map[string]bool)
	for _, option := range strings.Split(value, ",") {
		key, setting, ok := strings.Cut(option, "=")
		if !ok || setting == "" {
			return nil, fmt.Errorf("invalid --otel-logs option %q", option)
		}
		if key != "endpoint" && key != "grpc" && key != "ca" && key != "mtls" && key != "headers-secret" {
			return nil, fmt.Errorf("unknown --otel-logs option %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate --otel-logs option %q", key)
		}
		seen[key] = true
		settings[key] = setting
	}
	endpoint, err := url.Parse(settings["endpoint"])
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("--otel-logs endpoint must be an HTTPS URL without credentials, query, or fragment")
	}
	grpc := false
	if value := settings["grpc"]; value != "" {
		if value != "true" && value != "false" {
			return nil, errors.New("--otel-logs grpc must be true or false")
		}
		grpc = value == "true"
	}
	protocol := "http/protobuf"
	if grpc {
		protocol = "grpc"
	}
	mtls := false
	if value := settings["mtls"]; value != "" {
		if value != "true" && value != "false" {
			return nil, errors.New("--otel-logs mtls must be true or false")
		}
		mtls = value == "true"
	}
	uri := endpoint.EscapedPath()
	if !grpc && (uri == "" || uri == "/") {
		return nil, errors.New("--otel-logs HTTP endpoint must include the complete logs ingestion path")
	}
	if grpc && uri != "" && uri != "/" {
		return nil, errors.New("--otel-logs gRPC endpoint must not include a path")
	}
	if grpc {
		uri = "/v1/logs"
	}
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("--otel-logs endpoint has an invalid port")
	}
	headerSecret := ""
	headerProvider := ""
	if value := settings["headers-secret"]; value != "" {
		if value != "true" {
			return nil, errors.New("--otel-logs headers-secret must be true when set")
		}
		headerSecret, err = secrets.SystemName(selected.ClusterID, secrets.OTelLogsHeadersKey)
		if err != nil {
			return nil, fmt.Errorf("invalid --otel-logs headers-secret: %w", err)
		}
		headerProvider = selected.SecretsProvider
	}
	ca := settings["ca"]
	if value := settings["ca"]; value != "" {
		if _, parseErr := parseS3Object(value, "--otel-logs ca"); parseErr != nil {
			return nil, parseErr
		}
	}
	logs := &userdata.OTelLogs{Host: endpoint.Hostname(), Port: port, URI: uri, Protocol: protocol, MTLS: mtls, CA: ca, HeadersSecret: headerSecret, HeadersProvider: headerProvider}
	if err = userdata.ValidateOTelLogs(*logs); err != nil {
		return nil, fmt.Errorf("invalid --otel-logs configuration: %w", err)
	}
	return logs, nil
}

// parseWorkloadCAPublication validates an optional external S3 PEM destination.
func parseWorkloadCAPublication(value, clusterBucket string) (*infra.WorkloadCAPublication, error) {
	if value == "" {
		return nil, nil
	}
	object, err := parseS3Object(value, "--workload-ca-publish")
	if err != nil {
		return nil, err
	}
	if object.Bucket == clusterBucket && object.Key == "identity/ca.json" {
		return nil, errors.New("--workload-ca-publish must not overwrite identity/ca.json")
	}
	return &infra.WorkloadCAPublication{Bucket: object.Bucket, Key: object.Key}, nil
}

// parseS3Object validates one exact S3 object URL.
func parseS3Object(value, option string) (infra.S3Object, error) {
	destination, err := url.Parse(value)
	if err != nil {
		return infra.S3Object{}, fmt.Errorf("%s must be an s3://BUCKET/KEY URL", option)
	}
	key := strings.TrimPrefix(destination.Path, "/")
	if destination.Scheme != "s3" || destination.User != nil || destination.Host == "" || destination.Host != destination.Hostname() || destination.RawQuery != "" || destination.Fragment != "" || !s3BucketPattern.MatchString(destination.Host) || !s3KeyPattern.MatchString(key) || path.Clean(key) != key {
		return infra.S3Object{}, fmt.Errorf("%s must be an s3://BUCKET/KEY URL", option)
	}
	return infra.S3Object{Bucket: destination.Host, Key: key}, nil
}

// verifyOTelLogsSecret checks that the configured headers secret already exists.
func verifyOTelLogsSecret(ctx context.Context, client *cloud.Client, selected config.Context, logs *userdata.OTelLogs) error {
	if logs == nil || logs.HeadersSecret == "" {
		return nil
	}
	provider, err := secrets.ParseProvider(logs.HeadersProvider)
	if err != nil {
		return err
	}
	store, ok := client.SecretStores[provider]
	if !ok {
		return fmt.Errorf("secret provider %q is unavailable from cloud provider %q", provider, selected.Provider)
	}
	separator := strings.LastIndexByte(logs.HeadersSecret, '/')
	prefix, key := logs.HeadersSecret[:separator], logs.HeadersSecret[separator+1:]
	keys, err := store.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("list OTLP headers secrets: %w", err)
	}
	if !slices.Contains(keys, key) {
		return fmt.Errorf("OTLP headers secret %q does not exist", logs.HeadersSecret)
	}
	return nil
}

// addUserData renders and attaches each NodeGroup's bootstrap script.
func addUserData(selected config.Context, nodeGroups map[string]infra.NodeGroup, artifacts map[string][]dependencies.Artifact, otelLogs *userdata.OTelLogs, publication *infra.WorkloadCAPublication) error {
	pauseSource, err := images.ParseSource(pauseImage)
	if err != nil {
		return err
	}
	pause, err := registry.Mirror(pauseSource)
	if err != nil {
		return err
	}
	for name, nodeGroup := range nodeGroups {
		inputs := make([]userdata.Dependency, 0, len(artifacts[nodeGroup.Architecture]))
		for _, artifact := range artifacts[nodeGroup.Architecture] {
			if strings.HasPrefix(artifact.Key, "jool-") {
				continue
			}
			inputs = append(inputs, userdata.Dependency{Name: artifact.Name, ObjectKey: artifact.ObjectKey, Digest: artifact.Digest, Architecture: artifact.Architecture})
		}
		userData := userdata.UserData{Bucket: selected.Bucket, Region: selected.Region, Cluster: selected.ClusterID, NodeGroup: name, Architecture: nodeGroup.Architecture, PauseImage: pause.Name(), Dependencies: inputs, OTelLogs: otelLogs}
		if publication != nil {
			userData.WorkloadCAPublishBucket = publication.Bucket
			userData.WorkloadCAPublishKey = publication.Key
		}
		readable, err := userData.Render()
		if err != nil {
			return err
		}
		compressed, err := userdata.Compress(readable)
		if err != nil {
			return err
		}
		nodeGroup.UserData = base64.StdEncoding.EncodeToString(compressed)
		nodeGroups[name] = nodeGroup
	}
	return nil
}

// ensureCertificateAuthorities creates the workload and cluster CAs once.
func ensureCertificateAuthorities(ctx context.Context, store secrets.Manager, cluster string) error {
	key := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate workload CA key: %w", err)
	}
	if err := store.Create(ctx, "/"+cluster+"/_system/workload-ca-key", []byte(base64.StdEncoding.EncodeToString(key))); err != nil && !errors.Is(err, cloud.ErrExists) {
		return fmt.Errorf("create workload CA key: %w", err)
	}
	clusterCA, err := identity.Generate(cluster)
	if err != nil {
		return fmt.Errorf("generate cluster CA: %w", err)
	}
	if err = store.Create(ctx, "/"+cluster+identity.ClusterCAPathSuffix, clusterCA); err != nil && !errors.Is(err, cloud.ErrExists) {
		return fmt.Errorf("create cluster CA (workload CA may already exist): %w", err)
	}
	return nil
}
