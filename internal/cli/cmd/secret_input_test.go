// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/secrets"
	"github.com/spf13/cobra"
)

// TestReadSecretRejectsFileAndStdin verifies that the two explicit input modes are mutually exclusive.
func TestReadSecretRejectsFileAndStdin(t *testing.T) {
	t.Parallel()
	_, err := readSecret(&cobra.Command{}, "create", "api-key", true, "secret.txt")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("readSecret() error = %v, want mutual exclusion error", err)
	}
}

// TestReadSecretFromStdin verifies that --stdin uses the Cobra command input stream.
func TestReadSecretFromStdin(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("secret\nvalue"))
	value, err := readSecret(command, "create", "api-key", true, "")
	if err != nil {
		t.Fatalf("readSecret() error = %v", err)
	}
	if string(value) != "secret\nvalue" {
		t.Fatalf("readSecret() = %q, want %q", value, "secret\nvalue")
	}
}

// TestReadSecretFromFile verifies that --file reads the exact file bytes.
func TestReadSecretFromFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "secret-value")
	if err := os.WriteFile(path, []byte("secret\nvalue"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	value, err := readSecret(&cobra.Command{}, "update", "api-key", false, path)
	if err != nil {
		t.Fatalf("readSecret() error = %v", err)
	}
	if string(value) != "secret\nvalue" {
		t.Fatalf("readSecret() = %q, want %q", value, "secret\nvalue")
	}
}

// TestReadInteractiveSecretRejectsNonTerminal verifies that hidden input requires a terminal descriptor.
func TestReadInteractiveSecretRejectsNonTerminal(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	_, err = readInteractiveSecret(&cobra.Command{}, "create", "api-key", reader)
	if err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("readInteractiveSecret() error = %v, want non-terminal error", err)
	}
}

// TestSecretCommandsUseNamespaceFlag verifies every scoped secret operation exposes the namespace flag and shorthand.
func TestSecretCommandsUseNamespaceFlag(t *testing.T) {
	t.Parallel()
	root := secretCommand()
	if flag := root.PersistentFlags().Lookup("provider"); flag == nil || flag.DefValue != "" {
		t.Fatalf("secret --provider = %#v, want context default", flag)
	}
	if flag := root.PersistentFlags().Lookup("system"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("secret --system = %#v, want opt-in system scope", flag)
	}
	commands := root.Commands()
	for _, command := range commands {
		flags := command.InheritedFlags()
		if flags.Lookup("namespace") == nil || flags.ShorthandLookup("n") == nil {
			t.Errorf("secret %s does not expose --namespace/-n", command.Name())
		}
		if flags.Lookup("nodegroup") != nil || flags.Lookup("space") != nil || flags.ShorthandLookup("s") != nil {
			t.Errorf("secret %s exposes a legacy scope flag", command.Name())
		}
	}
}

// TestSystemSecretScopeAndAllowlist protects internal keys and conflicting workload flags.
func TestSystemSecretScopeAndAllowlist(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().String("namespace", "default", "")
	system := &secretScope{system: true, namespace: "default"}
	if err := validateSecretScope(command, system); err != nil {
		t.Fatalf("system scope rejected: %v", err)
	}
	if err := validateSecretKey(system, secrets.OTelLogsHeadersKey); err != nil {
		t.Fatalf("allowlisted system key rejected: %v", err)
	}
	for _, key := range []string{"cluster-ca", "workload-ca-key", "other"} {
		if err := validateSecretKey(system, key); err == nil {
			t.Errorf("internal system key %q was accepted", key)
		}
	}
	if got := manageableSystemKeys([]string{"cluster-ca", secrets.OTelLogsHeadersKey, "workload-ca-key"}); len(got) != 1 || got[0] != secrets.OTelLogsHeadersKey {
		t.Fatalf("manageableSystemKeys() = %q", got)
	}
	system.provider = string(secrets.AWSParameterStore)
	if err := validateSecretScope(command, system); err == nil {
		t.Fatal("system scope accepted --provider")
	}
	system.provider = ""
	if err := command.Flags().Set("namespace", "default"); err != nil {
		t.Fatal(err)
	}
	if err := validateSecretScope(command, system); err == nil {
		t.Fatal("system scope accepted explicit --namespace")
	}
	if err := validateSecretScope(&cobra.Command{}, &secretScope{}); err == nil {
		t.Fatal("workload scope accepted missing --for")
	}
}

// TestConnectDefaultsSecretsProvider verifies new contexts select Parameter Store unless overridden.
func TestConnectDefaultsSecretsProvider(t *testing.T) {
	flag := connectCommand().Flags().Lookup("secrets-provider")
	if flag == nil || flag.DefValue != string(secrets.AWSParameterStore) {
		t.Fatalf("connect --secrets-provider = %#v", flag)
	}
}

// TestSetupFlags verifies setup exposes its authoritative and opt-in inputs.
func TestSetupFlags(t *testing.T) {
	t.Parallel()
	flags := setupCommand().Flags()
	if flag := flags.Lookup("nodegroup"); flag == nil || flag.Value.Type() != "stringArray" {
		t.Fatalf("setup --nodegroup flag = %#v, want repeated stringArray", flag)
	}
	if flags.Lookup("space") != nil {
		t.Fatal("setup exposes a legacy --space flag")
	}
	if flags.Lookup("agent-source") == nil {
		t.Fatal("setup does not expose explicit agent source selection")
	}
	if flag := flags.Lookup("nat64"); flag == nil || flag.DefValue != "" || flag.NoOptDefVal != "instance-type=t4g.nano" || flag.Value.Type() != "string" {
		t.Fatalf("setup --nat64 flag = %#v, want opt-in configuration", flag)
	}
	if flag := flags.Lookup("otel-logs"); flag == nil || flag.DefValue != "" || flag.Value.Type() != "string" {
		t.Fatalf("setup --otel-logs flag = %#v, want opt-in configuration", flag)
	}
	bare := setupCommand().Flags()
	if err := bare.Parse([]string{"--nat64"}); err != nil || bare.Lookup("nat64").Value.String() != "instance-type=t4g.nano" {
		t.Fatalf("bare setup --nat64 = %q, %v", bare.Lookup("nat64").Value.String(), err)
	}
	explicit := setupCommand().Flags()
	if err := explicit.Parse([]string{"--nat64=instance-type=t4g.small"}); err != nil || explicit.Lookup("nat64").Value.String() != "instance-type=t4g.small" {
		t.Fatalf("explicit setup --nat64 = %q, %v", explicit.Lookup("nat64").Value.String(), err)
	}
	if fetchCommand().Flags().Lookup("agent-source") == nil {
		t.Fatal("fetch does not expose explicit agent source selection")
	}
}
