// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/config"
)

// TestUseWithoutArgumentListsContexts verifies stable context inventory and current selection output.
func TestUseWithoutArgumentListsContexts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Current: "zeta", Contexts: map[string]config.Context{
		"zeta":  {ClusterID: "zeta", Provider: "aws", Region: "us-west-2", Profile: "development", Bucket: "zeta-bucket"},
		"alpha": {ClusterID: "alpha", Provider: "aws", Region: "us-east-1", Bucket: "alpha-bucket"},
	}}
	if err = store.Save(state); err != nil {
		t.Fatal(err)
	}
	output := new(bytes.Buffer)
	command := useCommand()
	command.SetOut(output)
	if err = command.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("use output has %d lines, want 3:\n%s", len(lines), output)
	}
	if fields := strings.Fields(lines[0]); strings.Join(fields, " ") != "CURRENT NAME PROVIDER REGION PROFILE BUCKET" {
		t.Fatalf("header = %q", lines[0])
	}
	if fields := strings.Fields(lines[1]); strings.Join(fields, " ") != "- alpha aws us-east-1 - alpha-bucket" {
		t.Fatalf("first context = %q", lines[1])
	}
	if fields := strings.Fields(lines[2]); strings.Join(fields, " ") != "* zeta aws us-west-2 development zeta-bucket" {
		t.Fatalf("current context = %q", lines[2])
	}
}

// TestUseSelectsContext verifies successful activation is persisted and reported.
func TestUseSelectsContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Current: "alpha", Contexts: map[string]config.Context{
		"alpha": {ClusterID: "alpha"},
		"zeta":  {ClusterID: "zeta"},
	}}
	if err = store.Save(state); err != nil {
		t.Fatal(err)
	}
	output := new(bytes.Buffer)
	command := useCommand()
	command.SetArgs([]string{"zeta"})
	command.SetOut(output)
	if err = command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "Using context \"zeta\".\n" {
		t.Fatalf("output = %q", got)
	}
	selected, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	if selected.ClusterID != "zeta" {
		t.Fatalf("current context = %q, want zeta", selected.ClusterID)
	}
}

// TestUseWithoutContextsReportsEmptyState verifies an empty context store is clear to the user.
func TestUseWithoutContextsReportsEmptyState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	output := new(bytes.Buffer)
	command := useCommand()
	command.SetOut(output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "No contexts.\n" {
		t.Fatalf("output = %q, want %q", got, "No contexts.\n")
	}
}
