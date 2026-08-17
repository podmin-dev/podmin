// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDeployCommandsUseNodeGroupFlag verifies the deployment scope flag.
func TestDeployCommandsUseNodeGroupFlag(t *testing.T) {
	for _, command := range []*cobra.Command{initCommand(), deployCommand(), deleteCommand()} {
		flags := command.Flags()
		if flags.Lookup("nodegroup") == nil || flags.ShorthandLookup("g") == nil {
			t.Fatal("nodegroup flag or -g shorthand is missing")
		}
		if flags.Lookup("space") != nil || flags.ShorthandLookup("s") != nil || flags.ShorthandLookup("n") != nil {
			t.Fatal("legacy deployment scope shorthand remains")
		}
	}
}

// TestDeployHasNoDefaultManifestFile verifies local files are never selected implicitly.
func TestDeployHasNoDefaultManifestFile(t *testing.T) {
	t.Parallel()
	command := deployCommand()
	if value := command.Flags().Lookup("file").DefValue; value != "" {
		t.Fatalf("file default = %q, want empty", value)
	}
}

// TestDeployReadsExplicitManifest verifies --file disables the built-in manifest.
func TestDeployReadsExplicitManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	command := deployCommand()
	command.SetArgs([]string{"hello", "--image", "hello", "--nodegroup", "default", "--file", "missing.yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("Execute() error = %v, want missing explicit manifest", err)
	}
}

// TestDeployRequiresNodeGroupWithUsage verifies omitted invocation input prints help.
func TestDeployRequiresNodeGroupWithUsage(t *testing.T) {
	t.Parallel()
	output := new(bytes.Buffer)
	root := &cobra.Command{Use: "podmin", SilenceErrors: true}
	root.AddCommand(deployCommand())
	silenceUsageForRuntimeErrors(root)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"deploy", "hello", "--image", "hello"})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want missing nodegroup error")
	}
	if !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("output = %q, want usage", output)
	}
}

// TestValidateRequiresManifestFile verifies validation has no implicit input file.
func TestValidateRequiresManifestFile(t *testing.T) {
	t.Parallel()
	output := new(bytes.Buffer)
	root := &cobra.Command{Use: "podmin", SilenceErrors: true}
	root.AddCommand(validateCommand())
	silenceUsageForRuntimeErrors(root)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"validate"})
	if err := root.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want missing file error")
	}
	if !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("output = %q, want usage", output)
	}
}
