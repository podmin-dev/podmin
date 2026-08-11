// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/podmin-dev/podmin/internal/secrets"
)

// Context identifies a connected cluster.
type Context struct {
	ClusterID       string `json:"clusterID"`
	Provider        string `json:"provider"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Profile         string `json:"profile,omitempty"`
	SecretsProvider string `json:"secretsProvider"`
}

// State is the complete local context configuration.
type State struct {
	Current  string             `json:"current,omitempty"`
	Contexts map[string]Context `json:"contexts"`
}

// Store persists context state in one private file.
type Store struct{ Path string }

// ConfigDir returns Podmin's platform-aware configuration directory.
func ConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "podmin"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "podmin"), nil
	}
	return filepath.Join(home, ".podmin", "config"), nil
}

// CacheDir returns Podmin's platform-aware cache directory.
func CacheDir() (string, error) {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "podmin"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".cache", "podmin"), nil
	}
	return filepath.Join(home, ".podmin", "cache"), nil
}

// DefaultStore returns a store at the standard configuration path.
func DefaultStore() (*Store, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "contexts.json")}, nil
}

// Load reads state, returning empty state when no file exists.
func (s *Store) Load() (State, error) {
	state := State{Contexts: map[string]Context{}}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("read contexts: %w", err)
	}
	if state.Contexts == nil {
		state.Contexts = map[string]Context{}
	}
	for name, context := range state.Contexts {
		if context.SecretsProvider == "" {
			context.SecretsProvider = string(secrets.AWSParameterStore)
			state.Contexts[name] = context
		}
	}
	return state, nil
}

// Save atomically writes state with mode 0600 and syncs its directory.
func (s *Store) Save(state State) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".contexts-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, s.Path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(s.Path))
	if err != nil {
		return err
	}
	if err = d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// Current returns the selected context.
func (s *Store) Current() (Context, error) {
	state, err := s.Load()
	if err != nil {
		return Context{}, err
	}
	c, ok := state.Contexts[state.Current]
	if !ok {
		return Context{}, errors.New("no current context; run podmin connect or podmin use")
	}
	return c, nil
}
