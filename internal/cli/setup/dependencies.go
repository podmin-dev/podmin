// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/podmin-dev/podmin/internal/buildvars"
	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/podmin-dev/podmin/internal/cli/transfer"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/registry"
)

// syncDependencies publishes the desired files, images, and manifest for setup.
func syncDependencies(ctx context.Context, objects cloud.ObjectStore, nodeGroups map[string]infra.NodeGroup, cache, agentSource string, output io.Writer, status func(string) error) (map[string][]dependencies.Artifact, dependencies.Manifest, error) {
	if err := status("Reading published dependency manifest..."); err != nil {
		return nil, dependencies.Manifest{}, err
	}
	published, manifestVersion, err := readDependencyManifest(ctx, objects)
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	if err = status("Resolving dependency versions..."); err != nil {
		return nil, dependencies.Manifest{}, err
	}
	fetcher := &dependencies.Fetcher{CacheDir: filepath.Join(cache, "dependencies"), SourceDir: agentSource, AgentVersion: buildvars.BuildVersion()}
	artifacts, err := resolveDependencies(ctx, fetcher, nodeGroups)
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	imageCache, err := images.CacheRoot()
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	pauseSource, err := images.ParseSource(pauseImage)
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	pause, err := registry.Mirror(pauseSource)
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	pauseDigest, err := images.ResolveDigest(ctx, pauseImage)
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	desiredImage := dependencies.Image{Version: pauseVersion, Source: pauseImage, Path: pause.Context().RepositoryStr(), Digest: pauseDigest}
	pending, imagePending := pendingDependencies(published, artifacts, &desiredImage)
	if len(pending) > 0 || imagePending {
		err = tui.Run(output, "Preparing Podmin dependencies", func(progress tui.Progress) error {
			fetcher.Progress = progress
			if err = downloadDependencies(ctx, fetcher, artifacts, pending); err != nil {
				return fmt.Errorf("download dependencies: %w", err)
			}
			if !imagePending {
				return nil
			}
			progress(tui.Event{Type: tui.Status, Message: "Downloading dependency images..."})
			if err = images.PullIfMissing(ctx, pauseImage, pauseDigest, imageCache, progress); err != nil {
				return fmt.Errorf("download pause image: %w", err)
			}
			desiredImage.Size, err = images.CacheSize(pauseImage, imageCache)
			return err
		})
		if err != nil {
			return nil, dependencies.Manifest{}, err
		}
	} else if err = status("Dependencies are already published."); err != nil {
		return nil, dependencies.Manifest{}, err
	}
	if len(pending) > 0 || imagePending {
		err = tui.Run(output, "Publishing Podmin dependencies", func(progress tui.Progress) error {
			if len(pending) > 0 {
				progress(tui.Event{Type: tui.Status, Message: "Uploading bootstrap dependencies..."})
				if err = uploadDependencies(ctx, objects, pending, progress); err != nil {
					return err
				}
			}
			if imagePending {
				progress(tui.Event{Type: tui.Status, Message: "Uploading dependency images..."})
				if _, err = images.Mirror(ctx, pauseImage, imageCache, objects, progress); err != nil {
					return fmt.Errorf("upload pause image: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, dependencies.Manifest{}, err
		}
	}
	desired := dependencies.NewManifest(artifacts, map[string]dependencies.Image{"pause": desiredImage})
	if reflect.DeepEqual(published, desired) {
		return artifacts, desired, nil
	}
	if err = status("Publishing dependency manifest..."); err != nil {
		return nil, dependencies.Manifest{}, err
	}
	body, err := desired.Marshal()
	if err != nil {
		return nil, dependencies.Manifest{}, err
	}
	if err = objects.PutIfMatch(ctx, dependencies.ManifestPath, body, manifestVersion); err != nil {
		return nil, dependencies.Manifest{}, fmt.Errorf("publish dependency manifest: %w", err)
	}
	return artifacts, desired, nil
}

// resolveDependencies resolves one artifact set per architecture.
func resolveDependencies(ctx context.Context, fetcher *dependencies.Fetcher, nodeGroups map[string]infra.NodeGroup) (map[string][]dependencies.Artifact, error) {
	result := map[string][]dependencies.Artifact{}
	architectures := make([]string, 0, len(nodeGroups))
	for _, nodeGroup := range nodeGroups {
		if _, exists := result[nodeGroup.Architecture]; exists {
			continue
		}
		result[nodeGroup.Architecture] = nil
		architectures = append(architectures, nodeGroup.Architecture)
	}
	sort.Strings(architectures)
	for _, architecture := range architectures {
		artifacts, err := fetcher.Resolve(ctx, architecture)
		if err != nil {
			return nil, err
		}
		result[architecture] = artifacts
	}
	return result, nil
}

// pendingDependencies selects files and images absent from the published manifest.
func pendingDependencies(published dependencies.Manifest, artifacts map[string][]dependencies.Artifact, image *dependencies.Image) (map[string][]dependencies.Artifact, bool) {
	pending := map[string][]dependencies.Artifact{}
	for architecture, files := range artifacts {
		for index, artifact := range files {
			if dependencies.SameFile(published.Dependencies[artifact.Key][architecture], artifact) {
				artifacts[architecture][index].Size = published.Dependencies[artifact.Key][architecture].Size
				continue
			}
			pending[architecture] = append(pending[architecture], artifact)
		}
	}
	publishedImage, ok := published.Images["pause"]
	if ok && dependencies.SameImage(publishedImage, *image) {
		image.Size = publishedImage.Size
		return pending, false
	}
	return pending, true
}

// downloadDependencies downloads pending files and updates the complete artifact set.
func downloadDependencies(ctx context.Context, fetcher *dependencies.Fetcher, artifacts, pending map[string][]dependencies.Artifact) error {
	for architecture, files := range pending {
		downloaded, err := fetcher.Download(ctx, files)
		if err != nil {
			return err
		}
		byKey := make(map[string]dependencies.Artifact, len(downloaded))
		for _, artifact := range downloaded {
			byKey[artifact.Key] = artifact
		}
		for index, artifact := range artifacts[architecture] {
			if replacement, ok := byKey[artifact.Key]; ok {
				artifacts[architecture][index] = replacement
			}
		}
		pending[architecture] = downloaded
	}
	return nil
}

// uploadDependencies publishes fetched bootstrap artifacts.
func uploadDependencies(ctx context.Context, objects cloud.ObjectStore, artifactsByArchitecture map[string][]dependencies.Artifact, progress tui.Progress) error {
	architectures := make([]string, 0, len(artifactsByArchitecture))
	for architecture := range artifactsByArchitecture {
		architectures = append(architectures, architecture)
	}
	sort.Strings(architectures)
	for _, architecture := range architectures {
		for _, artifact := range artifactsByArchitecture[architecture] {
			name := filepath.Base(artifact.Path)
			body, err := os.Open(artifact.Path)
			if err != nil {
				return err
			}
			progress(tui.Event{Type: tui.Started, Name: name, Total: artifact.Size})
			reader := &transfer.Reader{Reader: body, Name: name, Total: artifact.Size, Progress: progress}
			uploadErr := objects.PutStream(ctx, artifact.ObjectKey, reader, artifact.Size)
			closeErr := body.Close()
			if uploadErr != nil {
				progress(tui.Event{Type: tui.Failed, Name: name, Err: uploadErr})
				return uploadErr
			}
			if closeErr != nil {
				progress(tui.Event{Type: tui.Failed, Name: name, Err: closeErr})
				return closeErr
			}
			progress(tui.Event{Type: tui.Done, Name: name, Current: artifact.Size, Total: artifact.Size})
		}
	}
	return nil
}

// readDependencyManifest returns the published manifest and its object version.
func readDependencyManifest(ctx context.Context, store cloud.ObjectStore) (dependencies.Manifest, string, error) {
	body, version, err := store.Get(ctx, dependencies.ManifestPath)
	if errors.Is(err, cloud.ErrNotFound) {
		return dependencies.Manifest{Version: 1, Dependencies: map[string]map[string]dependencies.File{}, Images: map[string]dependencies.Image{}}, "", nil
	}
	if err != nil {
		return dependencies.Manifest{}, "", err
	}
	manifest, err := dependencies.ParseManifest(body)
	return manifest, version, err
}

// cleanupDependencies applies count-and-age retention after successful setup.
func cleanupDependencies(ctx context.Context, store cloud.ObjectStore, manifest dependencies.Manifest) error {
	objects, err := store.List(ctx, "dependencies/")
	if err != nil {
		return err
	}
	retention := make([]dependencies.Object, 0, len(objects))
	for _, object := range objects {
		parts := strings.Split(object.Key, "/")
		if len(parts) != 3 {
			continue
		}
		group := parts[1]
		version := parts[2]
		for _, architecture := range []string{"amd64", "arm64"} {
			if strings.Contains(parts[2], "linux-"+architecture) {
				group += "/" + architecture
			}
		}
		retention = append(retention, dependencies.Object{Key: object.Key, Group: group, Version: version, Modified: object.Modified})
	}
	current := map[string]bool{dependencies.ManifestPath: true}
	for _, architectures := range manifest.Dependencies {
		for _, file := range architectures {
			current[file.Path] = true
		}
	}
	for _, object := range dependencies.Expired(retention, time.Now()) {
		if current[object.Key] {
			continue
		}
		if err = store.Delete(ctx, object.Key); err != nil {
			return err
		}
	}
	return nil
}
