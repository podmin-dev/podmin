// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

// TestAPIErrorCode verifies Smithy API errors are recognized through wrapping.
func TestAPIErrorCode(t *testing.T) {
	err := fmt.Errorf("put object: %w", &smithy.GenericAPIError{Code: "ConditionalRequestConflict", Message: "conflict"})
	if code := apiErrorCode(err); code != "ConditionalRequestConflict" {
		t.Fatalf("API error code = %q", code)
	}
	if code := apiErrorCode(errors.New("ordinary error")); code != "" {
		t.Fatalf("ordinary error code = %q", code)
	}
}

// TestReadBoundedEnforcesLimit verifies oversized provider control objects are rejected.
func TestReadBoundedEnforcesLimit(t *testing.T) {
	body, err := readBounded(strings.NewReader("1234"), 4)
	if err != nil || string(body) != "1234" {
		t.Fatalf("bounded read = %q, %v", body, err)
	}
	if _, err = readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized object was accepted")
	}
}
