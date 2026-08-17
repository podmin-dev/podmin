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

// TestDestroyPromptsForConfirmation verifies destruction can be declined interactively.
func TestDestroyPromptsForConfirmation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	context := config.Context{ClusterID: "demo", Provider: "aws", Region: "us-west-2", Bucket: "demo"}
	if err = store.Save(config.State{Current: "demo", Contexts: map[string]config.Context{"demo": context}}); err != nil {
		t.Fatal(err)
	}

	output := new(bytes.Buffer)
	command := destroyCommand()
	command.SetIn(strings.NewReader("n\n"))
	command.SetOut(output)
	err = command.Execute()
	if err == nil || err.Error() != "cluster destruction cancelled" {
		t.Fatalf("Execute() error = %v, want cancellation", err)
	}
	if want := `Permanently destroy cluster "demo"`; !strings.Contains(output.String(), want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
}
