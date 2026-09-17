// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestDebianKernelReadsTheConcreteCloudImage verifies metapackages cannot be mistaken for uname -r.
func TestDebianKernelReadsTheConcreteCloudImage(t *testing.T) {
	manifest := `{"items":[{"kind":"Build","data":{"info":{"arch":"arm64","release":"trixie","release_id":"13","vendor":"ec2","version":"20260914-2601"},"packages":[{"name":"linux-image-cloud-arm64"},{"name":"linux-image-6.12.107+deb13-cloud-arm64"}]}}]}`
	kernel, err := debianKernel(strings.NewReader(manifest), "arm64", "20260914-2601")
	if err != nil {
		t.Fatal(err)
	}
	if kernel != "6.12.107+deb13-cloud-arm64" {
		t.Fatalf("debianKernel() = %q", kernel)
	}
	if _, err = debianKernel(strings.NewReader(manifest), "amd64", "20260914-2601"); err == nil {
		t.Fatal("debianKernel() accepted a manifest for another architecture")
	}
}

// TestAllocateNodeGroupSubnetCIDRs verifies owned ranges are preserved and occupied ranges are skipped.
func TestAllocateNodeGroupSubnetCIDRs(t *testing.T) {
	base := netip.MustParsePrefix("2001:db8:1200::/56")
	occupied := map[string]bool{"2001:db8:1200::/64": true, "2001:db8:1200:1::/64": true}
	owned := map[string]string{"api": "2001:db8:1200:1::/64"}
	got, err := allocateNodeGroupSubnetCIDRs(base, occupied, owned, []string{"workers", "api"})
	if err != nil {
		t.Fatal(err)
	}
	if got["api"] != owned["api"] || got["workers"] != "2001:db8:1200:2::/64" {
		t.Fatalf("allocations = %#v", got)
	}
}

// TestAllocateNAT64SubnetCIDRs verifies owned ranges are retained and allocation avoids occupied subnets.
func TestAllocateNAT64SubnetCIDRs(t *testing.T) {
	vpc := netip.MustParsePrefix("10.0.0.0/24")
	occupied := []netip.Prefix{netip.MustParsePrefix("10.0.0.240/28")}
	got, err := allocateNAT64SubnetCIDRs(vpc, occupied, map[string]string{"us-west-2a": "10.0.0.224/28"}, []string{"us-west-2b", "us-west-2a"})
	if err != nil {
		t.Fatal(err)
	}
	if got["us-west-2a"] != "10.0.0.224/28" || got["us-west-2b"] != "10.0.0.208/28" {
		t.Fatalf("NAT64 CIDRs = %#v", got)
	}
}

// TestResolveNodeGroupZones verifies defaults, suffixes, full names, and unavailable zones.
func TestResolveNodeGroupZones(t *testing.T) {
	available := []string{"us-west-2a", "us-west-2b", "us-west-2c"}
	got, used, err := resolveNodeGroupZones(map[string]string{"api": "", "default": "", "workers": "b", "jobs": "us-west-2c"}, available)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"api": "us-west-2a", "default": "us-west-2a", "workers": "us-west-2b", "jobs": "us-west-2c"}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(used, available) {
		t.Fatalf("resolved zones = %v, used = %v; want %v, %v", got, used, want, available)
	}
	for _, requested := range []map[string]string{{"workers": "d"}, {"workers": "us-west-2d"}, {"workers": "us-east-1a"}} {
		if _, _, err = resolveNodeGroupZones(requested, available); err == nil {
			t.Fatalf("resolveNodeGroupZones(%v) accepted an unavailable zone", requested)
		}
	}
}
