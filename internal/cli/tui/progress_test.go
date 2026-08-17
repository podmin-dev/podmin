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

// TestViewReportsCompletion verifies the dashboard reports concise completion counts.
func TestViewReportsCompletion(t *testing.T) {
	t.Parallel()
	m := model{
		title: "Pushing image",
		items: map[string]item{"image": {Name: "image", Status: Done, Current: 2048, Total: 2048}},
		order: []string{"image"},
	}
	if output := m.View(); !strings.Contains(output, "1/1 complete") {
		t.Fatalf("View() = %q, want concise completion summary", output)
	}
}

// TestQueuedTransfersKeepOverallTotalStable verifies later phases do not grow the denominator.
func TestQueuedTransfersKeepOverallTotalStable(t *testing.T) {
	t.Parallel()
	m := model{items: map[string]item{}}
	m.apply(Event{Type: Queued, Name: "agent.tar.gz", Total: 2048})
	m.apply(Event{Type: Queued, Name: "pause", Total: 4096})
	_, total := m.overall()
	m.apply(Event{Type: Done, Name: "agent.tar.gz", Current: 2048, Total: 2048})
	m.apply(Event{Type: Started, Name: "pause", Total: 4096})
	if _, got := m.overall(); got != total || got != 6144 {
		t.Fatalf("overall total = %d after transfers started, want %d", got, total)
	}
}
