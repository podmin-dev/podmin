// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"net/netip"
	"testing"
)

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
