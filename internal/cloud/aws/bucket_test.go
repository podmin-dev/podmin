// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestPublicAccessBlockedRequiresEveryProtection verifies partial configurations need repair.
func TestPublicAccessBlockedRequiresEveryProtection(t *testing.T) {
	complete := &types.PublicAccessBlockConfiguration{
		BlockPublicAcls:       aws.Bool(true),
		BlockPublicPolicy:     aws.Bool(true),
		IgnorePublicAcls:      aws.Bool(true),
		RestrictPublicBuckets: aws.Bool(true),
	}
	if !publicAccessBlocked(complete) {
		t.Fatal("complete public access block reported incomplete")
	}
	complete.RestrictPublicBuckets = aws.Bool(false)
	if publicAccessBlocked(complete) {
		t.Fatal("partial public access block reported complete")
	}
	if publicAccessBlocked(nil) {
		t.Fatal("missing public access block reported complete")
	}
}
