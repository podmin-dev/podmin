// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
)

// TestCurrentNAT64RefreshSelectsTheCurrentSetup verifies old successes cannot satisfy a new setup.
func TestCurrentNAT64RefreshSelectsTheCurrentSetup(t *testing.T) {
	generation := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	old := generation.Add(-time.Minute)
	current := generation.Add(time.Minute)
	newest := generation.Add(2 * time.Minute)
	refreshes := []types.InstanceRefresh{
		{InstanceRefreshId: aws.String("old"), StartTime: &old, Status: types.InstanceRefreshStatusSuccessful},
		{InstanceRefreshId: aws.String("current"), StartTime: &current, Status: types.InstanceRefreshStatusInProgress},
		{InstanceRefreshId: aws.String("newest"), StartTime: &newest, Status: types.InstanceRefreshStatusPending},
	}
	if got := currentNAT64Refresh(refreshes, "", generation); got == nil || aws.ToString(got.InstanceRefreshId) != "newest" {
		t.Fatalf("new refresh = %#v, want newest", got)
	}
	if got := currentNAT64Refresh(refreshes, "current", generation); got == nil || aws.ToString(got.InstanceRefreshId) != "current" {
		t.Fatalf("selected refresh = %#v, want current", got)
	}
	if got := currentNAT64Refresh(refreshes, "missing", generation); got != nil {
		t.Fatalf("missing refresh = %#v, want nil", got)
	}
}
