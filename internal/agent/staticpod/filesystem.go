// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/podmin-dev/podmin/internal/secrets"
)

// stagedFile is a synced same-directory temporary file awaiting publication.
type stagedFile struct {
	path      string
	temporary string
}

// stagedChange is one prepared filesystem publication.
type stagedChange interface {
	commit() error
	discard()
}

// stagedSymlink is an atomically replaceable same-directory symbolic link.
type stagedSymlink struct {
	path      string
	temporary string
}

// stageSymlink creates a temporary relative symbolic link awaiting publication.
func stageSymlink(path, target string) (*stagedSymlink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	temporary := filepath.Join(filepath.Dir(path), ".podmin-link-"+filepath.Base(target))
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return nil, err
	}
	return &stagedSymlink{path: path, temporary: temporary}, nil
}

// commit atomically publishes the staged symbolic link.
func (s *stagedSymlink) commit() error {
	if err := os.Rename(s.temporary, s.path); err != nil {
		return err
	}
	s.temporary = ""
	return syncDirectory(filepath.Dir(s.path))
}

// discard removes an unpublished temporary symbolic link.
func (s *stagedSymlink) discard() {
	if s.temporary != "" {
		_ = os.Remove(s.temporary)
	}
}

// stageFile writes and syncs a temporary file without changing the live path.
func stageFile(path string, body []byte, mode os.FileMode) (*stagedFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".podmin-*")
	if err != nil {
		return nil, err
	}
	temporary := file.Name()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return &stagedFile{path: path, temporary: temporary}, nil
}

// commit atomically replaces the live path with staged bytes.
func (f *stagedFile) commit() error {
	if err := os.Rename(f.temporary, f.path); err != nil {
		return err
	}
	f.temporary = ""
	return syncDirectory(filepath.Dir(f.path))
}

// discard removes an uncommitted temporary file.
func (f *stagedFile) discard() {
	if f.temporary != "" {
		_ = os.Remove(f.temporary)
	}
}

// syncDirectory persists directory entry changes when the directory exists.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

// cleanup removes stale Pod files from the Podmin-owned directory and provider trees only after publication.
func cleanup(staticDir, secretDir string, keep map[string]bool, desired map[string]map[string]map[string]bool) error {
	entries, err := os.ReadDir(staticDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yaml") && !keep[strings.TrimSuffix(entry.Name(), ".yaml")] {
			if err = os.Remove(filepath.Join(staticDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	entries, err = os.ReadDir(secretDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && !keep[entry.Name()] {
			if err = os.RemoveAll(filepath.Join(secretDir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() {
			for _, name := range []string{string(secrets.AWSParameterStore), string(secrets.AWSSecretsManager)} {
				provider := filepath.Join(secretDir, entry.Name(), name)
				files, readErr := os.ReadDir(provider)
				if readErr != nil && !os.IsNotExist(readErr) {
					return readErr
				}
				for _, file := range files {
					if !file.IsDir() && !desired[entry.Name()][name][file.Name()] {
						if err = os.Remove(filepath.Join(provider, file.Name())); err != nil {
							return err
						}
					}
				}
				if len(desired[entry.Name()][name]) == 0 {
					if err = os.RemoveAll(provider); err != nil {
						return err
					}
				}
			}
		}
	}
	if err = syncDirectory(staticDir); err != nil {
		return err
	}
	return syncDirectory(secretDir)
}

// cleanupIdentityGenerations retains the desired and one previous identity generation per Pod.
func cleanupIdentityGenerations(secretDir string, desired map[string]string) error {
	for pod, current := range desired {
		root := filepath.Join(secretDir, pod, "identity-generations")
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var previous os.DirEntry
		var previousTime int64
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == current {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if previous == nil || info.ModTime().UnixNano() > previousTime {
				previous, previousTime = entry, info.ModTime().UnixNano()
			}
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != current && (previous == nil || entry.Name() != previous.Name()) {
				if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
