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

	"github.com/podmin-dev/podmin/internal/cli/tui"
	"golang.org/x/mod/semver"
)

const maxConcurrentDownloads = 4

// Artifact is one verified architecture-specific bootstrap dependency.
type Artifact struct {
	Key          string
	Name         string
	Version      string
	Architecture string
	URL          string
	ObjectKey    string
	Digest       string
	Path         string
	Size         int64
}

// Fetcher resolves and caches the canonical bootstrap dependencies.
type Fetcher struct {
	Client       *http.Client
	CacheDir     string
	SourceDir    string
	AgentVersion string
	Progress     tui.Progress
	versions     map[string]string
}

// Fetch downloads and verifies every dependency required by architecture.
func (f *Fetcher) Fetch(ctx context.Context, architecture string) ([]Artifact, error) {
	artifacts, err := f.Resolve(ctx, architecture)
	if err != nil {
		return nil, err
	}
	return f.Download(ctx, artifacts)
}

// Resolve selects versions and checksums without downloading artifacts.
func (f *Fetcher) Resolve(ctx context.Context, architecture string) ([]Artifact, error) {
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
	errorsByIndex := make([]error, len(Catalog))
	f.concurrent(ctx, len(Catalog), func(ctx context.Context, index int) {
		dependency := Catalog[index]
		artifact, err := f.resolveArtifact(ctx, dependency, f.versions[dependency.Key], architecture)
		if err != nil {
			errorsByIndex[index] = fmt.Errorf("resolve %s: %w", dependency.Key, err)
			return
		}
		artifacts[index] = artifact
	})
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

// Download validates cached artifacts and downloads only missing or corrupt files.
func (f *Fetcher) Download(ctx context.Context, artifacts []Artifact) ([]Artifact, error) {
	result := make([]Artifact, len(artifacts))
	errorsByIndex := make([]error, len(artifacts))
	f.concurrent(ctx, len(artifacts), func(ctx context.Context, index int) {
		artifact, err := f.download(ctx, artifacts[index])
		if err != nil {
			errorsByIndex[index] = fmt.Errorf("fetch %s: %w", artifacts[index].Key, err)
			return
		}
		result[index] = artifact
	})
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// concurrent runs count indexed operations with bounded concurrency.
func (f *Fetcher) concurrent(ctx context.Context, count int, run func(context.Context, int)) {
	limit := make(chan struct{}, maxConcurrentDownloads)
	var group sync.WaitGroup
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			run(ctx, index)
		}()
	}
	group.Wait()
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
