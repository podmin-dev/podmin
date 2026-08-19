// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/deploy"
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

// TestDeployConvenienceValidation verifies environment inheritance and duplicate rejection.
func TestDeployConvenienceValidation(t *testing.T) {
	t.Setenv("INHERITED", "from-process")
	env, err := parseEnv([]string{"EXPLICIT=one=two", "INHERITED"})
	if err != nil || env["EXPLICIT"] != "one=two" || env["INHERITED"] != "from-process" {
		t.Fatalf("parseEnv() = %#v, %v", env, err)
	}
	for _, values := range [][]string{{"DUP=one", "DUP=two"}, {"not-valid=value"}, {"MISSING_FROM_PROCESS"}} {
		if _, err = parseEnv(values); err == nil {
			t.Fatalf("parseEnv(%v) accepted invalid input", values)
		}
	}
	if _, err = parseSecretKeys([]string{"token", "token"}); err == nil {
		t.Fatal("parseSecretKeys accepted a duplicate")
	}
	ports, err := parsePorts([]string{"443:8443,8082:8082", "9090:9091"})
	if err != nil || len(ports) != 3 || ports[0].Port != 443 || ports[0].TargetPort != 8443 || ports[1].Port != 8082 || ports[2].TargetPort != 9091 {
		t.Fatalf("parsePorts() = %#v, %v", ports, err)
	}
	for _, values := range [][]string{{"443"}, {"0:8443"}, {"443:65536"}, {"443:8443", "443:9443"}, {"443:8443,"}} {
		if _, err = parsePorts(values); err == nil {
			t.Fatalf("parsePorts(%v) accepted invalid input", values)
		}
	}
}

// TestDeployRejectsConvenienceFlagsWithFile verifies explicit manifests remain authoritative.
func TestDeployRejectsConvenienceFlagsWithFile(t *testing.T) {
	command := deployCommand()
	command.SetArgs([]string{"hello", "--nodegroup", "default", "--file", "missing.yaml", "--env", "PORT=80"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be used with --file") {
		t.Fatalf("Execute() error = %v, want convenience flag conflict", err)
	}
}

// TestDeployPortRequiresService verifies port mappings cannot silently expose a workload.
func TestDeployPortRequiresService(t *testing.T) {
	command := deployCommand()
	command.SetArgs([]string{"hello", "--nodegroup", "default", "--image", "hello", "--port", "443:8443"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "requires --service") {
		t.Fatalf("Execute() error = %v, want --service requirement", err)
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

// TestValidateServiceRequiresAServiceDocument verifies the file assertion does not generate one.
func TestValidateServiceRequiresAServiceDocument(t *testing.T) {
	directory := t.TempDir()
	path := directory + "/daemonset.yaml"
	body := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: hello}\nspec:\n  template:\n    spec:\n      nodeSelector: {podmin.dev/nodegroup: default}\n      containers: [{name: hello, image: hello}]\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	command := validateCommand()
	command.SetArgs([]string{"--file", path, "--service"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "manifest to contain a Service") {
		t.Fatalf("Execute() error = %v", err)
	}
}

// TestInstallRejectsUnknownComponentAndInvalidNodeGroup verifies local invocation checks precede cloud access.
func TestInstallRejectsUnknownComponentAndInvalidNodeGroup(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"other", "--nodegroup", "default"}, want: "unknown component"},
		{args: []string{"cloudflared", "--nodegroup", "Not-Valid"}, want: "invalid nodegroup ID"},
	} {
		command := installCommand()
		command.SetArgs(test.args)
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Execute(%v) error = %v, want %q", test.args, err, test.want)
		}
	}
}

// TestWriteListingsRendersTheDesiredStateTable verifies stable human-readable inventory output.
func TestWriteListingsRendersTheDesiredStateTable(t *testing.T) {
	output := new(bytes.Buffer)
	listings := []deploy.Listing{{Name: "hello", Namespace: "default", NodeGroup: "default", Service: "hello", Origin: "workload"}}
	if err := writeListings(output, listings); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"NAME", "NAMESPACE", "NODEGROUP", "SERVICE", "ORIGIN", "hello", "default", "workload"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("list output lacks %q: %q", expected, output.String())
		}
	}
}
