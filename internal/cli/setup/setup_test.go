// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import "testing"

// TestParseNodeGroups validates defaults, duplicates, and malformed names.
func TestParseNodeGroups(t *testing.T) {
	nodeGroups, err := parseNodeGroups([]string{"workers", "api,size=2,instance-type=m7g.large"})
	if err != nil {
		t.Fatal(err)
	}
	if nodeGroups["workers"].Size != 1 || nodeGroups["api"].Size != 2 || nodeGroups["api"].InstanceType != "m7g.large" {
		t.Fatalf("NodeGroups = %#v", nodeGroups)
	}
	for _, values := range [][]string{nil, {"workers", "workers"}, {"Not Valid"}} {
		if _, err = parseNodeGroups(values); err == nil {
			t.Fatalf("parseNodeGroups(%q) succeeded", values)
		}
	}
}
