// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/podmin-dev/podmin/internal/secrets"
)

// TestStoreRoundTrip verifies private atomic state persistence.
func TestStoreRoundTrip(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "config", "contexts.json")}
	want := Context{ClusterID: "demo", Provider: "aws", Region: "us-west-2", Bucket: "bucket", SecretsProvider: string(secrets.AWSSecretsManager)}
	if err := s.Save(State{Current: "demo", Contexts: map[string]Context{"demo": want}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	got, err := s.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("context = %#v", got)
	}
}

// TestLoadDefaultsSecretsProvider upgrades contexts written before provider selection existed.
func TestLoadDefaultsSecretsProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{"current":"demo","contexts":{"demo":{"clusterID":"demo","provider":"aws","region":"us-west-2","bucket":"bucket"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	context, err := (&Store{Path: path}).Current()
	if err != nil {
		t.Fatal(err)
	}
	if context.SecretsProvider != string(secrets.AWSParameterStore) {
		t.Fatalf("SecretsProvider = %q", context.SecretsProvider)
	}
}

// TestDirectoriesHonorXDG verifies explicit XDG configuration on every platform.
func TestDirectoriesHonorXDG(t *testing.T) {
	tests := []struct {
		variable string
		lookup   func() (string, error)
	}{
		{"XDG_CONFIG_HOME", ConfigDir},
		{"XDG_CACHE_HOME", CacheDir},
	}
	for _, test := range tests {
		t.Run(test.variable, func(t *testing.T) {
			t.Setenv(test.variable, "/xdg")
			got, err := test.lookup()
			if err != nil {
				t.Fatal(err)
			}
			if got != filepath.Join("/xdg", "podmin") {
				t.Fatalf("directory = %q", got)
			}
		})
	}
}
