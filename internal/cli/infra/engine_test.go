// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSkipsEmptyPlan verifies an empty plan is neither confirmed nor applied.
func TestRunSkipsEmptyPlan(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	log := filepath.Join(t.TempDir(), "commands")
	command := filepath.Join(t.TempDir(), "tofu")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$COMMAND_LOG\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMAND_LOG", log)
	t.Setenv("PODMIN_TF_CMD", command)
	var output bytes.Buffer
	if err := Run(context.Background(), Variables{ClusterID: "test", Bucket: "bucket", Region: "region"}, false, false, strings.NewReader("y\n"), &output, &output); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(commands), "init\nplan\n"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if strings.Contains(output.String(), "Apply this") {
		t.Fatalf("unexpected confirmation prompt: %s", output.String())
	}
}

// TestPrepareKeepsProviderFiles verifies regeneration retains initialized providers and their lock file.
func TestPrepareKeepsProviderFiles(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := WorkDir("test")
	if err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(dir, ".terraform", "providers", "provider")
	lock := filepath.Join(dir, ".terraform.lock.hcl")
	for path, data := range map[string]string{
		provider:                          "provider",
		lock:                              "lock",
		filepath.Join(dir, "obsolete.tf"): "obsolete",
		filepath.Join(dir, "podmin.plan"): "plan",
	} {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = Prepare(Variables{ClusterID: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{provider, lock, filepath.Join(dir, "network.tf"), filepath.Join(dir, "podmin.auto.tfvars.json")} {
		if _, err = os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(dir, "obsolete.tf"), filepath.Join(dir, "podmin.plan")} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, got %v", path, err)
		}
	}
}
