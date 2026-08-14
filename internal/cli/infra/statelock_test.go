// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import "testing"

// TestStateLockIDRecognizesLockDiagnostics verifies lock errors are distinguished from other failures.
func TestStateLockIDRecognizesLockDiagnostics(t *testing.T) {
	diagnostic := "Error: Error acquiring the state lock\n\nLock Info:\n  ID:        df07e9da-e151-b0b4-4749-c18107353b7a\n  Operation: OperationTypeApply\n"
	if id := stateLockID(diagnostic); id != "df07e9da-e151-b0b4-4749-c18107353b7a" {
		t.Fatalf("lock ID = %q", id)
	}
	if id := stateLockID("Error: ordinary plan failure\n  ID: unrelated"); id != "" {
		t.Fatalf("ordinary error produced lock ID %q", id)
	}
}
