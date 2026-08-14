// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunTextReportsFiles verifies redirected output remains informative.
func TestRunTextReportsFiles(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	err := Run(&output, "Transfers", func(progress Progress) error {
		progress(Event{Type: Status, Message: "Downloading files..."})
		progress(Event{Type: Checking, Name: "agent.tar.gz", Total: 2048})
		progress(Event{Type: Started, Name: "agent.tar.gz", Total: 2048})
		progress(Event{Type: Done, Name: "agent.tar.gz", Current: 2048, Total: 2048})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Downloading files...", "Checking agent.tar.gz (2.0 KiB)", "Transferring agent.tar.gz (2.0 KiB)", "Transferred agent.tar.gz (2.0 KiB)"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output = %q, want %q", output.String(), expected)
		}
	}
}
