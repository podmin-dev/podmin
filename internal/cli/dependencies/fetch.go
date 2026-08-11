// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Artifact is one verified architecture-specific bootstrap dependency.
type Artifact struct {
	Name, Version, Architecture, ObjectKey, Digest, Path string
}

// Fetcher resolves and caches the canonical bootstrap dependencies.
type Fetcher struct {
	Client       *http.Client
	CacheDir     string
	SourceDir    string
	AgentVersion string
	versions     map[string]string
}

// Fetch downloads and verifies every dependency required by architecture.
func (f *Fetcher) Fetch(ctx context.Context, architecture string) ([]Artifact, error) {
	if architecture != "amd64" && architecture != "arm64" {
		return nil, fmt.Errorf("unsupported architecture %q", architecture)
	}
	if f.Client == nil {
		f.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if f.CacheDir == "" {
		return nil, errors.New("dependency cache directory is required")
	}
	if err := os.MkdirAll(f.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("create dependency cache: %w", err)
	}
	if f.versions == nil {
		versions, err := f.resolve(ctx)
		if err != nil {
			return nil, err
		}
		f.versions = versions
	}
	artifacts := make([]Artifact, len(Catalog))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsByIndex := make([]error, len(Catalog))
	limit := make(chan struct{}, 4)
	var group sync.WaitGroup
	for index, dependency := range Catalog {
		group.Add(1)
		go func() {
			defer group.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			artifact, err := f.fetch(ctx, dependency, f.versions[dependency.Key], architecture)
			if err != nil {
				errorsByIndex[index] = fmt.Errorf("fetch %s: %w", dependency.Key, err)
				cancel()
				return
			}
			artifacts[index] = artifact
		}()
	}
	group.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

// resolve selects one version of every dependency for this fetch batch.
func (f *Fetcher) resolve(ctx context.Context) (map[string]string, error) {
	versions := make(map[string]string, len(Catalog))
	for _, dependency := range Catalog {
		var version string
		var err error
		switch dependency.ReleaseStyle {
		case agentRelease:
			if f.SourceDir != "" {
				_, err = sourceRoot(f.SourceDir)
				if err == nil {
					version = "source"
				}
			} else if semver.IsValid(f.AgentVersion) && semver.Prerelease(f.AgentVersion) == "" {
				version = f.AgentVersion
			} else {
				version, err = Resolve(ctx, f.Client, dependency)
			}
		case gvisorRelease:
			version, err = f.resolveDatedRelease(ctx, dependency.Releases)
		default:
			version, err = Resolve(ctx, f.Client, dependency)
		}
		if err != nil {
			return nil, err
		}
		versions[dependency.Key] = version
	}
	return versions, nil
}
