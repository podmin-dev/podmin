// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package coordinator

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/api"
	"github.com/podmin-dev/podmin/internal/agent/dataplane"
	"github.com/podmin-dev/podmin/internal/agent/service"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podplane/s3lect"
)

// fakeElector is a fixed leadership view for coordinator unit tests.
type fakeElector struct{ leader bool }

// Start accepts startup.
func (f *fakeElector) Start(context.Context) error { return nil }

// Stop accepts shutdown.
func (f *fakeElector) Stop() error { return nil }

// IsLeader returns the fixed state.
func (f *fakeElector) IsLeader() bool { return f.leader }

// WaitForLeadership returns immediately for the fixed leader.
func (f *fakeElector) WaitForLeadership(context.Context) error { return nil }

// WaitForNextElection returns the fixed status.
func (f *fakeElector) WaitForNextElection(context.Context, time.Time) (*s3lect.LeadershipStatus, error) {
	return f.GetLeadershipStatus(), nil
}

// TestCoordinatorRejectsConflictingSameNodeGroupContracts verifies one NodeGroup cannot publish divergent desired state.
func TestCoordinatorRejectsConflictingSameNodeGroupContracts(t *testing.T) {
	controller := service.NewController("cluster", "leader", netip.MustParseAddr("2001:db8::1"), service.NewServer(), dataplane.New(netip.MustParsePrefix("2001:db8:1::/80")))
	coordinator, err := New(Config{NodeID: "node", Cluster: "cluster", NodeGroup: "leader", IPv6Prefix: netip.MustParsePrefix("2001:db8:1::/80"), Elector: &fakeElector{leader: true}, Storage: fakeStorage{}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.leaderReady.Store(true)
	first := &api.Hello{NodeId: "one", Cluster: "cluster", NodeGroup: "remote", SessionId: "one", ConfigDigest: strings.Repeat("0", 128)}
	second := &api.Hello{NodeId: "two", Cluster: "cluster", NodeGroup: "remote", SessionId: "two", ConfigDigest: strings.Repeat("1", 128)}
	if _, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: first}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: second}}, true); err == nil {
		t.Fatal("conflicting same-NodeGroup digest was accepted")
	}
}

// TestCoordinatorRejectsConflictingCrossNodeGroupService verifies Service ownership is cluster-wide.
func TestCoordinatorRejectsConflictingCrossNodeGroupService(t *testing.T) {
	controller := service.NewController("cluster", "leader", netip.MustParseAddr("2001:db8::1"), service.NewServer(), dataplane.New(netip.MustParsePrefix("2001:db8:1::/80")))
	coordinator, err := New(Config{NodeID: "node", Cluster: "cluster", NodeGroup: "leader", IPv6Prefix: netip.MustParsePrefix("2001:db8:1::/80"), Elector: &fakeElector{leader: true}, Storage: fakeStorage{}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.leaderReady.Store(true)
	contract := func(nodeGroup string) []*api.Service {
		return []*api.Service{{Name: "web", Namespace: "product", NodeGroup: nodeGroup, Fqdn: "web.product.svc.cluster.local", Vip: dataplane.DeriveVIP("cluster", "product", "web").String()}}
	}
	first := &api.Hello{NodeId: "one", Cluster: "cluster", NodeGroup: "blue", SessionId: "one", ConfigDigest: strings.Repeat("0", 128), Services: contract("blue")}
	second := &api.Hello{NodeId: "two", Cluster: "cluster", NodeGroup: "green", SessionId: "two", ConfigDigest: strings.Repeat("1", 128), Services: contract("green")}
	if _, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: first}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: second}}, true); err == nil {
		t.Fatal("conflicting cross-NodeGroup Service was accepted")
	}
}

// LeaderID returns the fixed leader ID.
func (f *fakeElector) LeaderID() string { return "node" }

// GetLeadershipStatus returns a complete fixed status.
func (f *fakeElector) GetLeadershipStatus() *s3lect.LeadershipStatus {
	return &s3lect.LeadershipStatus{IsLeader: f.leader, LeaderID: "node", LeaderAddr: "[2001:db8::1]:8081"}
}

// EnablePeerMode accepts unused peer configuration.
func (f *fakeElector) EnablePeerMode([]byte) error { return nil }

// UpdateConfig accepts unused dynamic configuration.
func (f *fakeElector) UpdateConfig(s3lect.ElectorConfig) error { return nil }

// GetConfig returns an empty test configuration.
func (f *fakeElector) GetConfig() *s3lect.ElectorConfig { return &s3lect.ElectorConfig{} }

// fakeStorage is an unused durable store for session-only tests.
type fakeStorage struct{}

// Get reports no durable object.
func (fakeStorage) Get(context.Context, string) ([]byte, string, error) {
	return nil, "", s3lect.ErrStorageNotFound
}

// PutIfMatch accepts a conditional write.
func (fakeStorage) PutIfMatch(context.Context, string, []byte, string) error { return nil }

// coordinatorDataplane records the most recently applied complete model.
type coordinatorDataplane struct{ snapshot dataplane.Snapshot }

// Reconcile records one complete dataplane model.
func (d *coordinatorDataplane) Reconcile(_ context.Context, snapshot dataplane.Snapshot) error {
	d.snapshot = snapshot
	return nil
}

// TestLocalTransitionPreservesRemoteNodeGroups verifies repeated empty local reconciliation cannot erase global state.
func TestLocalTransitionPreservesRemoteNodeGroups(t *testing.T) {
	plane := new(coordinatorDataplane)
	dns := service.NewServer()
	controller := service.NewController("cluster", "local", netip.MustParseAddr("2001:db8::1"), dns, plane)
	coordinator, err := New(Config{NodeID: "node", Cluster: "cluster", NodeGroup: "local", IPv6Prefix: netip.MustParsePrefix("2001:db8:1::/80"), Elector: &fakeElector{}, Storage: fakeStorage{}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	port := &api.TargetPort{Protocol: "TCP", ServicePort: 80, TargetPort: 8080}
	remote := &api.Service{Name: "web", Namespace: "default", NodeGroup: "remote", Fqdn: "web.default.svc.cluster.local", Vip: dataplane.DeriveVIP("cluster", "default", "web").String(), Ports: []*api.TargetPort{port}, Backends: []*api.Endpoint{{Service: "web", Namespace: "default", NodeGroup: "remote", PodUid: "pod", Address: "2001:db8:1::20", Ports: []*api.TargetPort{port}}}}
	coordinator.snapshot = &api.Snapshot{Generation: 1, Revision: 1, Services: []*api.Service{remote}}
	coordinator.staleCleared = true
	controller.SetServices(nil)
	controller.SetServices(nil)
	if err := coordinator.applyLocalTransition(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(plane.snapshot.Services) != 1 || plane.snapshot.Services[0].VIP.String() != remote.Vip {
		t.Fatalf("remote service was erased: %#v", plane.snapshot)
	}
	if len(plane.snapshot.Services[0].Ports[0].Backends) != 0 {
		t.Fatalf("expired remote backend was restored: %#v", plane.snapshot)
	}
}

// TestCoordinatorRejectsReplacedSession verifies an old stream cannot mutate a replacement node session.
func TestCoordinatorRejectsReplacedSession(t *testing.T) {
	controller := service.NewController("cluster", "workers", netip.MustParseAddr("2001:db8::1"), service.NewServer(), dataplane.New(netip.MustParsePrefix("2001:db8:1::/80")))
	coordinator, err := New(Config{NodeID: "node", Cluster: "cluster", NodeGroup: "workers", IPv6Prefix: netip.MustParsePrefix("2001:db8:1::/80"), Elector: &fakeElector{leader: true}, Storage: fakeStorage{}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.generation = 1
	coordinator.leaderReady.Store(true)
	digest := controller.Digest()
	for _, session := range []string{"old", "new"} {
		_, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: &api.Hello{NodeId: "peer", Cluster: "cluster", NodeGroup: "workers", SessionId: session, ConfigDigest: digest}}}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = coordinator.Handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_NodeState{NodeState: &api.NodeState{SessionId: "old", Sequence: 1, ConfigDigest: digest}}})
	if err == nil {
		t.Fatal("replaced session was accepted")
	}
}

// TestCoordinatorRejectsEndpointOutsideDelegatedPrefix verifies authorized prefixes bind endpoint publication.
func TestCoordinatorRejectsEndpointOutsideDelegatedPrefix(t *testing.T) {
	controller := service.NewController("cluster", "workers", netip.MustParseAddr("2001:db8::1"), service.NewServer(), dataplane.New(netip.MustParsePrefix("2001:db8:1::/80")))
	controller.SetServices([]manifest.Service{{Name: "web", Namespace: "default", Ports: []manifest.ServicePort{{Protocol: "TCP", Port: 80, TargetPort: 8080}}}})
	coordinator, err := New(Config{NodeID: "leader", Cluster: "cluster", NodeGroup: "workers", IPv6Prefix: netip.MustParsePrefix("2001:db8:1::/80"), Elector: &fakeElector{leader: true}, Storage: fakeStorage{}, Controller: controller})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.leaderReady.Store(true)
	hello := &api.Hello{NodeId: "leader", Cluster: "cluster", NodeGroup: "workers", SessionId: "session", ConfigDigest: controller.Digest(), Services: controller.Contract(), Ipv6Prefix: "2001:db8:1::/80"}
	if _, err = coordinator.handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: hello}}, true); err != nil {
		t.Fatal(err)
	}
	state := &api.NodeState{SessionId: "session", Sequence: 1, ConfigDigest: controller.Digest(), Endpoints: []*api.Endpoint{{Service: "web", Namespace: "default", NodeGroup: "workers", PodUid: "pod", Address: "2001:db8:2::1", Ports: controller.Contract()[0].Ports}}}
	if _, err = coordinator.Handle(context.Background(), &api.ClientMessage{Message: &api.ClientMessage_NodeState{NodeState: state}}); err == nil {
		t.Fatal("endpoint outside delegated prefix was accepted")
	}
}
