// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUsageOnlyForInvocationErrors verifies runtime failures omit command help.
func TestUsageOnlyForInvocationErrors(t *testing.T) {
	t.Parallel()

	validationOutput := new(bytes.Buffer)
	validationRoot := &cobra.Command{Use: "test", SilenceErrors: true}
	validationCommand := &cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return nil }}
	validationCommand.Flags().String("required", "", "required value")
	_ = validationCommand.MarkFlagRequired("required")
	validationRoot.AddCommand(validationCommand)
	silenceUsageForRuntimeErrors(validationRoot)
	validationRoot.SetOut(validationOutput)
	validationRoot.SetErr(validationOutput)
	validationRoot.SetArgs([]string{"run"})
	if err := validationRoot.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want required flag error")
	}
	if !strings.Contains(validationOutput.String(), "Usage:") {
		t.Fatalf("validation output = %q, want usage", validationOutput)
	}

	runtimeOutput := new(bytes.Buffer)
	runtimeRoot := &cobra.Command{Use: "test", SilenceErrors: true}
	runtimeRoot.AddCommand(&cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error {
		return errors.New("runtime failure")
	}})
	silenceUsageForRuntimeErrors(runtimeRoot)
	runtimeRoot.SetOut(runtimeOutput)
	runtimeRoot.SetErr(runtimeOutput)
	runtimeRoot.SetArgs([]string{"run"})
	if err := runtimeRoot.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want runtime error")
	}
	if strings.Contains(runtimeOutput.String(), "Usage:") {
		t.Fatalf("runtime output = %q, want no usage", runtimeOutput)
	}
}
