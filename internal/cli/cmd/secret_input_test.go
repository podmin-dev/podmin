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

// TestConnectDefaultsSecretsProvider verifies new contexts select Parameter Store unless overridden.
func TestConnectDefaultsSecretsProvider(t *testing.T) {
	flag := connectCommand().Flags().Lookup("secrets-provider")
	if flag == nil || flag.DefValue != string(secrets.AWSParameterStore) {
		t.Fatalf("connect --secrets-provider = %#v", flag)
	}
}

// TestSetupUsesRepeatedNodeGroupFlag verifies setup exposes the authoritative repeated flag without a legacy alias.
func TestSetupUsesRepeatedNodeGroupFlag(t *testing.T) {
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
	if fetchCommand().Flags().Lookup("agent-source") == nil {
		t.Fatal("fetch does not expose explicit agent source selection")
	}
}
