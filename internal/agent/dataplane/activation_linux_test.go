// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package dataplane

import (
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/cilium/ebpf"
)

// TestBPFSpecification verifies the committed object has the required TCX program and map contracts.
func TestBPFSpecification(t *testing.T) {
	spec, err := loadBpf()
	if err != nil {
		t.Fatal(err)
	}
	program := spec.Programs[bpfProgPodminIngress]
	if program == nil || program.Type != ebpf.SchedCLS || program.AttachType != ebpf.AttachTCXIngress {
		t.Fatalf("unexpected ingress program: %#v", program)
	}
	for _, name := range []string{bpfMapServices, bpfMapBackends, bpfMapConfig, bpfMapServiceVips, bpfMapForwardFlows, bpfMapReverseFlows} {
		if spec.Maps[name] == nil {
			t.Fatalf("missing map %q", name)
		}
	}
	if spec.Maps[bpfMapForwardFlows].Type != ebpf.LRUHash || spec.Maps[bpfMapReverseFlows].Type != ebpf.LRUHash {
		t.Fatal("conntrack maps are not LRU hashes")
	}
	if spec.Maps[bpfMapServices].MaxEntries != maxTableEntries || spec.Maps[bpfMapBackends].MaxEntries != maxTableEntries {
		t.Fatalf("table map capacities = %d, %d", spec.Maps[bpfMapServices].MaxEntries, spec.Maps[bpfMapBackends].MaxEntries)
	}
}

// TestRouteInterfaces verifies Pod host-route filtering, deduplication, and default metric selection.
func TestRouteInterfaces(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "routes")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const zero = "00000000000000000000000000000000"
	const suffix = " 00 " + zero + " 00000020 00000000 00000000 00000001 "
	rows := "fd000000000000000000000000000001 80" + suffix + "pod1\n" +
		"fd000000000000000000000000000002 80" + suffix + "pod1\n" +
		"fd010000000000000000000000000001 80" + suffix + "outside\n" +
		"fd000000000000000000000000000000 40" + suffix + "network\n" +
		zero + " 00" + suffix + "slow0\n" +
		zero + " 00 00 00 " + zero + " 00000010 00000000 00000000 00000001 fast0\n"
	if _, err = file.WriteString(rows); err != nil {
		t.Fatal(err)
	}
	got, err := routeInterfaces(file.Name(), netip.MustParsePrefix("fd00::/64"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fast0", "pod1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interfaces = %v, want %v", got, want)
	}
}

// TestRouteInterfaceErrors verifies malformed candidate rows and missing route files fail discovery.
func TestRouteInterfaceErrors(t *testing.T) {
	prefix := netip.MustParsePrefix("fd00::/64")
	for _, row := range []string{
		"not-hex 80 00 00 00 00000001 00 00 00 pod0\n",
		"fd000000000000000000000000000001 80 00 00 00 badmetric 00 00 00 pod0\n",
		"00000000000000000000000000000000 00\n",
	} {
		path := t.TempDir() + "/routes"
		if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := routeInterfaces(path, prefix); err == nil {
			t.Fatalf("malformed candidate accepted: %q", row)
		}
	}
	if _, err := routeInterfaces(t.TempDir()+"/missing", prefix); err == nil {
		t.Fatal("missing route file accepted")
	}
}

// TestBPFVerifier asks the running kernel verifier to load the generated collection when explicitly enabled.
func TestBPFVerifier(t *testing.T) {
	if os.Getenv("PODMIN_TEST_BPF") == "" {
		t.Skip("set PODMIN_TEST_BPF=1 on a privileged Linux host")
	}
	var objects bpfObjects
	if err := loadBpfObjects(&objects, nil); err != nil {
		t.Fatal(err)
	}
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
}
