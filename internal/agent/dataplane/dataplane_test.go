// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dataplane

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"testing"
)

// TestDeriveVIP verifies stable namespaced ULA derivation.
func TestDeriveVIP(t *testing.T) {
	first := DeriveVIP("cluster", "default", "api")
	if first != DeriveVIP("cluster", "default", "api") || first == DeriveVIP("cluster", "other", "api") {
		t.Fatal("VIP derivation is not deterministic and namespaced")
	}
	if got := first.As16(); got[0] != 0xfd || got[1] != 0x50 {
		t.Fatalf("VIP is outside the fixed ULA prefix: %s", first)
	}
}

// TestCompileProtocolsPortsAndSelection verifies deterministic service maps.
func TestCompileProtocolsPortsAndSelection(t *testing.T) {
	vip := netip.MustParseAddr("fd50:6f64:6d69::1")
	snapshot := Snapshot{NodeIPv6: netip.MustParseAddr("2001:db8::10"), Services: []Service{{VIP: vip, Ports: []Port{
		{Protocol: ProtocolUDP, Port: 53, Backends: []Backend{{Address: netip.MustParseAddr("2001:db8::3"), Port: 8053}, {Address: netip.MustParseAddr("2001:db8::2"), Port: 8053}}},
		{Protocol: ProtocolTCP, Port: 80},
	}}}}
	tables, err := Compile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	udp := ServiceKey{VIP: vip.As16(), Protocol: ProtocolUDP, Port: 53}
	if got := tables.Services[udp]; got.BackendCount != 2 {
		t.Fatalf("UDP backend count = %d", got.BackendCount)
	}
	if _, ok := tables.SelectBackend(ServiceKey{VIP: vip.As16(), Protocol: ProtocolTCP, Port: 80}, []byte("flow")); ok {
		t.Fatal("backend selected for fail-closed service")
	}
	first, ok := tables.SelectBackend(udp, []byte("flow"))
	if !ok {
		t.Fatal("no UDP backend selected")
	}
	second, _ := tables.SelectBackend(udp, []byte("flow"))
	if first != second {
		t.Fatal("selection is not deterministic")
	}
}

// TestCompileTableLimits verifies exact BPF capacities are accepted and overflows are rejected.
func TestCompileTableLimits(t *testing.T) {
	vip := netip.MustParseAddr("fd50:6f64:6d69::1")
	ports := make([]Port, 0, maxTableEntries+1)
	for protocol := ProtocolTCP; protocol <= ProtocolUDP; protocol += ProtocolUDP - ProtocolTCP {
		for port := uint16(1); port <= maxTableEntries/2; port++ {
			ports = append(ports, Port{Protocol: protocol, Port: port})
		}
	}
	snapshot := Snapshot{NodeIPv6: netip.MustParseAddr("2001:db8::1"), Services: []Service{{VIP: vip, Ports: ports}}}
	if tables, err := Compile(snapshot); err != nil || len(tables.Services) != maxTableEntries {
		t.Fatalf("exact service capacity: %d, %v", len(tables.Services), err)
	}
	snapshot.Services[0].Ports = append(snapshot.Services[0].Ports, Port{Protocol: ProtocolTCP, Port: maxTableEntries/2 + 1})
	if _, err := Compile(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("service overflow error = %v", err)
	}
	backends := make([]Backend, maxTableEntries+1)
	for index := range backends {
		backends[index] = Backend{Address: netip.MustParseAddr("2001:db8::2"), Port: 80}
	}
	snapshot.Services = []Service{{VIP: vip, Ports: []Port{{Protocol: ProtocolTCP, Port: 80, Backends: backends[:maxTableEntries]}}}}
	if tables, err := Compile(snapshot); err != nil || len(tables.Backends) != maxTableEntries {
		t.Fatalf("exact backend capacity: %d, %v", len(tables.Backends), err)
	}
	snapshot.Services[0].Ports[0].Backends = backends
	if _, err := Compile(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("backend overflow error = %v", err)
	}
}

// TestEmptyReconcileAndOwnership verifies no-op teardown and defensive copies.
func TestEmptyReconcileAndOwnership(t *testing.T) {
	r := New(netip.MustParsePrefix("fd00::/64"))
	if err := r.Reconcile(context.Background(), Snapshot{}); err != nil {
		t.Fatal(err)
	}
	copy := r.Tables()
	copy.Backends = append(copy.Backends, BackendValue{Port: 1})
	copy.Services[ServiceKey{}] = ServiceValue{}
	owned := r.Tables()
	if len(owned.Backends) != 0 || len(owned.Services) != 0 {
		t.Fatal("caller mutated reconciler-owned tables")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestActivationRejected verifies that eBPF activation remains Linux-only.
func TestActivationRejected(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("activation requires a privileged TCX-capable host")
	}
	r := New(netip.MustParsePrefix("fd00::/64"))
	err := r.Reconcile(context.Background(), Snapshot{NodeIPv6: netip.MustParseAddr("2001:db8::1"), Services: []Service{{VIP: netip.MustParseAddr("fd50::1"), Ports: []Port{{Protocol: ProtocolTCP, Port: 80}}}}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("activation error = %v", err)
	}
}

// TestActivationPrefixValidation verifies activation requires a canonical IPv6 Pod prefix.
func TestActivationPrefixValidation(t *testing.T) {
	snapshot := Snapshot{NodeIPv6: netip.MustParseAddr("2001:db8::1"), Services: []Service{{VIP: netip.MustParseAddr("fd50::1"), Ports: []Port{{Protocol: ProtocolTCP, Port: 80}}}}}
	for _, prefix := range []netip.Prefix{{}, netip.MustParsePrefix("192.0.2.0/24"), netip.PrefixFrom(netip.MustParseAddr("fd00::1"), 64)} {
		if err := New(prefix).Reconcile(context.Background(), snapshot); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("prefix %v activation error = %v", prefix, err)
		}
	}
}
