// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/dataplane"
	"github.com/podmin-dev/podmin/internal/agent/pods"
	"github.com/podmin-dev/podmin/internal/agent/service"
)

// daemonTestDataplane accepts controller snapshots used by daemon lifecycle tests.
type daemonTestDataplane struct{}

// Reconcile accepts one complete dataplane snapshot.
func (*daemonTestDataplane) Reconcile(context.Context, dataplane.Snapshot) error { return nil }

// TestRunDaemonRejectsInvalidPodPrefixes verifies AWS delegated-prefix constraints at the process boundary.
func TestRunDaemonRejectsInvalidPodPrefixes(t *testing.T) {
	for _, prefix := range []netip.Prefix{{}, netip.MustParsePrefix("fe80::/80"), netip.MustParsePrefix("2001:db8::/64"), netip.PrefixFrom(netip.MustParseAddr("2001:db8::1"), 80)} {
		err := RunDaemon(context.Background(), DaemonConfig{Provider: "aws", Bucket: "bucket", Region: "region", Cluster: "cluster", NodeGroup: "nodegroup", IPv6Prefix: prefix})
		if err == nil || err.Error() != "invalid required configuration" {
			t.Fatalf("prefix %v error = %v", prefix, err)
		}
	}
}

// TestRunPodsControllerPublishesAndRefreshes verifies every kubelet update refreshes endpoint and interface state.
func TestRunPodsControllerPublishesAndRefreshes(t *testing.T) {
	controller := service.NewController("cluster", "blue", netip.MustParseAddr("2001:db8::10"), service.NewServer(), &daemonTestDataplane{})
	var refreshes atomic.Int32
	watch := func(_ context.Context, publish func(pods.Snapshot)) {
		publish(pods.Snapshot{"ready": {UID: "ready", Namespace: "default", Labels: map[string]string{"app": "web"}, Address: netip.MustParseAddr("2001:db8::20"), Eligible: true}})
	}
	if err := runPodsControllerWithWatcher(context.Background(), controller, func() { refreshes.Add(1) }, watch); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	controller.SetServices(nil)
	if got := len(controller.NodeState("session", 1).Endpoints); got != 0 {
		t.Fatalf("endpoints without Services = %d", got)
	}
}

// TestRunComponentsFatalCancelsSiblings verifies first-error propagation and sibling shutdown.
func TestRunComponentsFatalCancelsSiblings(t *testing.T) {
	want := errors.New("fatal")
	siblingStopped := make(chan struct{})
	var stops atomic.Int32
	components := []component{
		{name: "fatal", run: func(context.Context) error { return want }, stop: func(context.Context) error { stops.Add(1); return nil }},
		{name: "sibling", run: func(ctx context.Context) error { <-ctx.Done(); close(siblingStopped); return nil }, stop: func(context.Context) error { stops.Add(1); return nil }},
	}
	if got := runComponents(context.Background(), components, 0); !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
	select {
	case <-siblingStopped:
	default:
		t.Fatal("sibling did not observe cancellation")
	}
	if stops.Load() != 2 {
		t.Fatalf("stop calls = %d, want 2", stops.Load())
	}
}

// TestRunComponentsGraceful verifies caller cancellation returns no fatal error.
func TestRunComponentsGraceful(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	worker := component{name: "worker", run: func(ctx context.Context) error { close(started); <-ctx.Done(); return nil }}
	result := make(chan error, 1)
	go func() { result <- runComponents(ctx, []component{worker}, 0) }()
	<-started
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("graceful error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("graceful shutdown timed out")
	}
}

// TestRunComponentsRejectsUnexpectedCleanExit verifies silent component loss is fatal.
func TestRunComponentsRejectsUnexpectedCleanExit(t *testing.T) {
	worker := component{name: "worker", run: func(context.Context) error { return nil }}
	err := runComponents(context.Background(), []component{worker}, 0)
	if err == nil || err.Error() != "worker stopped unexpectedly" {
		t.Fatalf("error = %v, want unexpected stop", err)
	}
}
