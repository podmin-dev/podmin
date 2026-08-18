// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// buildAgent cross-compiles the agent from an explicitly selected Podmin checkout.
func (f Fetcher) buildAgent(ctx context.Context, dependency Dependency, architecture string) (Artifact, error) {
	root, err := sourceRoot(f.SourceDir)
	if err != nil {
		return Artifact{}, err
	}
	temporary, err := os.MkdirTemp(f.CacheDir, ".podmin-agent-*")
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	binary := filepath.Join(temporary, "podmin-agent")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "./cmd/podmin-agent")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+architecture)
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		return Artifact{}, fmt.Errorf("build podmin-agent: %w: %s", buildErr, strings.TrimSpace(string(output)))
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		return Artifact{}, err
	}
	body, err = tarGzip("podmin-agent", body)
	if err != nil {
		return Artifact{}, err
	}
	sum, _, err := digest("sha512", bytes.NewReader(body))
	if err != nil {
		return Artifact{}, err
	}
	version := "source-" + sum[:12]
	dir := filepath.Join(f.CacheDir, dependency.Key, version, architecture)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(dir, dependency.ObjectName)
	if err = atomicFile(path, body, 0600); err != nil {
		return Artifact{}, err
	}
	object := fmt.Sprintf("podmin-agent-%s-linux-%s.tar.gz", version, architecture)
	return Artifact{Key: dependency.Key, Name: dependency.ObjectName, Version: version, Architecture: architecture, URL: "source:podmin-agent", ObjectKey: "dependencies/podmin-agent/" + object, Digest: "sha512:" + sum, Path: path, Size: int64(len(body))}, nil
}

// sourceRoot finds the Podmin module containing start.
func sourceRoot(start string) (string, error) {
	if start == "" {
		return "", errors.New("agent source directory is required")
	}
	directory, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		module, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
		if readErr == nil && modfile.ModulePath(module) == "github.com/podmin-dev/podmin" {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("podmin source checkout not found")
		}
		directory = parent
	}
}
