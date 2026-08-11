// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/api"
	"github.com/podmin-dev/podmin/internal/agent/dataplane"
	"github.com/podmin-dev/podmin/internal/agent/pods"
	"github.com/podmin-dev/podmin/internal/manifest"
	"google.golang.org/protobuf/proto"
)

// fakeDataplane records the most recently applied complete model.
type fakeDataplane struct{ snapshot dataplane.Snapshot }

// Reconcile records one complete dataplane model.
func (f *fakeDataplane) Reconcile(_ context.Context, snapshot dataplane.Snapshot) error {
	f.snapshot = snapshot
	return nil
}

// TestClusterSnapshotIncludesMultipleNodeGroups verifies global identities, VIPs, and leased backends are deterministic.
func TestClusterSnapshotIncludesMultipleNodeGroups(t *testing.T) {
	blue := NewController("cluster", "blue", netip.MustParseAddr("2001:db8::10"), NewServer(), new(fakeDataplane))
	green := NewController("cluster", "green", netip.MustParseAddr("2001:db8::11"), NewServer(), new(fakeDataplane))
	blue.SetServices([]manifest.Service{{Name: "web", Namespace: "product", Ports: []manifest.ServicePort{{Name: "http", Protocol: "TCP", Port: 80, TargetPort: 8080}}}})
	green.SetServices([]manifest.Service{{Name: "db", Namespace: "data", Ports: []manifest.ServicePort{{Name: "sql", Protocol: "TCP", Port: 5432, TargetPort: 5432}}}})
	blue.SetPods(pods.Snapshot{"blue-pod": {UID: "blue-pod", Namespace: "product", Address: netip.MustParseAddr("2001:db8::20"), Eligible: true}})
	green.SetPods(pods.Snapshot{"green-pod": {UID: "green-pod", Namespace: "data", Address: netip.MustParseAddr("2001:db8::21"), Eligible: true}})
	blueHello := &api.Hello{NodeGroup: "blue", ConfigDigest: blue.Digest(), Services: blue.Contract()}
	greenHello := &api.Hello{NodeGroup: "green", ConfigDigest: green.Digest(), Services: green.Contract()}
	peers := []Peer{{Hello: greenHello, State: green.NodeState("green", 1)}, {Hello: blueHello, State: blue.NodeState("blue", 1)}}
	first, err := BuildClusterSnapshot("cluster", 1, 1, "leader", peers)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildClusterSnapshot("cluster", 1, 1, "leader", []Peer{peers[1], peers[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(first, second) {
		t.Fatalf("snapshot depends on peer ordering:\n%v\n%v", first, second)
	}
	if len(first.Services) != 2 || first.Services[0].Fqdn != "db.data.svc.cluster.local" || first.Services[1].Fqdn != "web.product.svc.cluster.local" {
		t.Fatalf("unexpected global identities: %#v", first.Services)
	}
	if first.Services[0].Vip != dataplane.DeriveVIP("cluster", "data", "db").String() || first.Services[1].Vip != dataplane.DeriveVIP("cluster", "product", "web").String() || len(first.Services[0].Backends) != 1 || len(first.Services[1].Backends) != 1 {
		t.Fatalf("unexpected global VIPs or backends: %#v", first.Services)
	}
}

// TestClusterSnapshotRejectsDuplicateGlobalService verifies NodeGroups cannot share a Service identity.
func TestClusterSnapshotRejectsDuplicateGlobalService(t *testing.T) {
	service := func(nodeGroup string) *api.Service {
		return &api.Service{Name: "web", Namespace: "product", NodeGroup: nodeGroup, Fqdn: "web.product.svc.cluster.local", Vip: dataplane.DeriveVIP("cluster", "product", "web").String()}
	}
	peers := []Peer{
		{Hello: &api.Hello{NodeGroup: "blue", ConfigDigest: strings.Repeat("0", 128), Services: []*api.Service{service("blue")}}},
		{Hello: &api.Hello{NodeGroup: "green", ConfigDigest: strings.Repeat("1", 128), Services: []*api.Service{service("green")}}},
	}
	if _, err := BuildClusterSnapshot("cluster", 1, 1, "leader", peers); err == nil {
		t.Fatal("duplicate global Service identity was accepted")
	}
}

// TestProtobufBoundariesRejectNilRepeatedMessages verifies malformed wire messages return errors.
func TestProtobufBoundariesRejectNilRepeatedMessages(t *testing.T) {
	digest := strings.Repeat("0", 128)
	controller := NewController("cluster", "blue", netip.MustParseAddr("2001:db8::10"), NewServer(), new(fakeDataplane))
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "contract Service", run: func() error { return ValidateContract("cluster", "blue", digest, []*api.Service{nil}) }},
		{name: "contract port", run: func() error {
			return ValidateContract("cluster", "blue", digest, []*api.Service{{NodeGroup: "blue", Namespace: "default", Name: "api", Ports: []*api.TargetPort{nil}}})
		}},
		{name: "node endpoint", run: func() error { return ValidateNodeState("blue", nil, &api.NodeState{Endpoints: []*api.Endpoint{nil}}) }},
		{name: "node endpoint port", run: func() error {
			return ValidateNodeState("blue", nil, &api.NodeState{Endpoints: []*api.Endpoint{{Ports: []*api.TargetPort{nil}}}})
		}},
		{name: "Hello Service", run: func() error {
			_, err := BuildClusterSnapshot("cluster", 1, 1, "leader", []Peer{{Hello: &api.Hello{NodeGroup: "blue", Services: []*api.Service{nil}}}})
			return err
		}},
		{name: "Hello port", run: func() error {
			_, err := BuildClusterSnapshot("cluster", 1, 1, "leader", []Peer{{Hello: &api.Hello{NodeGroup: "blue", Services: []*api.Service{{Ports: []*api.TargetPort{nil}}}}}})
			return err
		}},
		{name: "snapshot Service", run: func() error {
			return controller.Apply(context.Background(), &api.Snapshot{Services: []*api.Service{nil}})
		}},
		{name: "snapshot port", run: func() error {
			return controller.Apply(context.Background(), &api.Snapshot{Services: []*api.Service{{Ports: []*api.TargetPort{nil}}}})
		}},
		{name: "snapshot backend", run: func() error {
			return controller.Apply(context.Background(), &api.Snapshot{Services: []*api.Service{{Backends: []*api.Endpoint{nil}}}})
		}},
		{name: "snapshot backend port", run: func() error {
			return controller.Apply(context.Background(), &api.Snapshot{Services: []*api.Service{{Backends: []*api.Endpoint{{Ports: []*api.TargetPort{nil}}}}}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("nil repeated message was accepted")
			}
		})
	}
}

// TestPodOnlyNodeGroupAppliesRemoteServices verifies an empty local contract does not gate remote global state.
func TestPodOnlyNodeGroupAppliesRemoteServices(t *testing.T) {
	plane := new(fakeDataplane)
	dns := NewServer()
	controller := NewController("cluster", "pods", netip.MustParseAddr("2001:db8::10"), dns, plane)
	remote := &api.Service{Name: "web", Namespace: "default", NodeGroup: "blue", Fqdn: "web.default.svc.cluster.local", Vip: dataplane.DeriveVIP("cluster", "default", "web").String(), Ports: []*api.TargetPort{{Protocol: "TCP", ServicePort: 80, TargetPort: 8080}}}
	if err := controller.Apply(context.Background(), &api.Snapshot{Services: []*api.Service{remote}}); err != nil {
		t.Fatal(err)
	}
	if len(plane.snapshot.Services) != 1 || len(dns.endpoints) != 1 {
		t.Fatalf("remote service was not retained: %#v %#v", plane.snapshot, dns.endpoints)
	}
	if err := controller.Apply(context.Background(), &api.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if len(plane.snapshot.Services) != 0 || len(dns.endpoints) != 0 {
		t.Fatal("empty global snapshot did not clear DNS and dataplane")
	}
}

// TestControllerSelectsReadyPodsAndKeepsVIPStable verifies selector, readiness, and health-independent DNS identity.
func TestControllerSelectsReadyPodsAndKeepsVIPStable(t *testing.T) {
	plane := new(fakeDataplane)
	dns := NewServer()
	controller := NewController("cluster", "blue", netip.MustParseAddr("2001:db8::10"), dns, plane)
	controller.SetServices([]manifest.Service{{Name: "web", Namespace: "default", Selector: map[string]string{"app": "web"}, Ports: []manifest.ServicePort{{Name: "http", Protocol: "TCP", Port: 80, TargetPort: 8080}}}})
	controller.SetPods(pods.Snapshot{"ready": {UID: "ready", Namespace: "default", Labels: map[string]string{"app": "web"}, Address: netip.MustParseAddr("2001:db8::1"), Eligible: true}, "wrong-namespace": {UID: "wrong-namespace", Namespace: "other", Labels: map[string]string{"app": "web"}, Address: netip.MustParseAddr("2001:db8::4"), Eligible: true}, "unready": {UID: "unready", Namespace: "default", Labels: map[string]string{"app": "web"}, Address: netip.MustParseAddr("2001:db8::2")}, "other": {UID: "other", Namespace: "default", Labels: map[string]string{"app": "other"}, Address: netip.MustParseAddr("2001:db8::3"), Eligible: true}})
	state := controller.NodeState("session", 1)
	if len(state.Endpoints) != 1 || state.Endpoints[0].PodUid != "ready" {
		t.Fatalf("unexpected selected endpoints: %v", state.Endpoints)
	}
	withBackend := controller.BuildSnapshot(1, 1, "leader", []*api.NodeState{state})
	empty := controller.BuildSnapshot(1, 2, "leader", nil)
	if withBackend.Services[0].Vip != empty.Services[0].Vip || len(empty.Services[0].Backends) != 0 {
		t.Fatalf("VIP changed with backend health: %#v %#v", withBackend, empty)
	}
	if err := controller.Apply(context.Background(), withBackend); err != nil {
		t.Fatal(err)
	}
	if len(plane.snapshot.Services) != 1 || len(plane.snapshot.Services[0].Ports[0].Backends) != 1 {
		t.Fatalf("matching local backend was discarded: %#v", plane.snapshot)
	}
}

// TestControllerOverlaysDesiredServiceChanges verifies local desired state supersedes stale snapshots.
func TestControllerOverlaysDesiredServiceChanges(t *testing.T) {
	plane := new(fakeDataplane)
	dns := NewServer()
	controller := NewController("cluster", "blue", netip.MustParseAddr("2001:db8::10"), dns, plane)
	controller.SetServices([]manifest.Service{{Name: "old", Namespace: "default", Ports: []manifest.ServicePort{{Protocol: "TCP", Port: 80, TargetPort: 8080}}}})
	controller.SetPods(pods.Snapshot{"ready": {UID: "ready", Namespace: "default", Address: netip.MustParseAddr("2001:db8::1"), Eligible: true}})
	old := controller.BuildSnapshot(1, 1, "leader", []*api.NodeState{controller.NodeState("session", 1)})
	if err := controller.Apply(context.Background(), old); err != nil {
		t.Fatalf("apply old service: %v", err)
	}
	controller.SetServices([]manifest.Service{{Name: "new", Namespace: "default", Ports: []manifest.ServicePort{{Protocol: "UDP", Port: 53, TargetPort: 5353}}}})
	if err := controller.Apply(context.Background(), old); err != nil {
		t.Fatalf("overlay replacement service: %v", err)
	}
	if len(plane.snapshot.Services) != 1 || plane.snapshot.Services[0].VIP != dataplane.DeriveVIP("cluster", "default", "new") {
		t.Fatalf("unexpected replacement snapshot: %#v", plane.snapshot)
	}
	if len(plane.snapshot.Services[0].Ports) != 1 || len(plane.snapshot.Services[0].Ports[0].Backends) != 0 {
		t.Fatalf("replacement retained backends: %#v", plane.snapshot)
	}
	if _, found := dns.endpoints["old.default.svc.cluster.local"]; found {
		t.Fatal("stale DNS identity was retained")
	}
	if _, found := dns.endpoints["new.default.svc.cluster.local"]; !found {
		t.Fatal("new DNS identity was not published")
	}
	controller.SetServices(nil)
	if err := controller.Apply(context.Background(), controller.BuildSnapshot(1, 3, "leader", nil)); err != nil {
		t.Fatalf("apply empty service state: %v", err)
	}
	if len(plane.snapshot.Services) != 0 || len(dns.endpoints) != 0 {
		t.Fatalf("empty desired state was not applied: %#v %#v", plane.snapshot, dns.endpoints)
	}
}

// TestControllerBroadcastsChanges verifies independent consumers cannot steal notifications.
func TestControllerBroadcastsChanges(t *testing.T) {
	controller := NewController("cluster", "workers", netip.MustParseAddr("2001:db8::10"), NewServer(), new(fakeDataplane))
	first, closeFirst := controller.Subscribe()
	defer closeFirst()
	second, closeSecond := controller.Subscribe()
	defer closeSecond()
	controller.SetPods(pods.Snapshot{})
	for name, changed := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatalf("%s subscriber missed state change", name)
		}
	}
}
