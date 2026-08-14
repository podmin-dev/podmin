// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/validate"
	"github.com/podmin-dev/podmin/internal/cli/transfer"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podplane/ocimage/pkg/store"
)

// ResolveDigest returns the current digest of a remote image reference.
func ResolveDigest(ctx context.Context, source string) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	descriptor, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", source, err)
	}
	return descriptor.Digest.String(), nil
}

// Pull imports the complete source image or multi-platform index into cacheRoot.
func Pull(ctx context.Context, source string, cacheRoot string, progress tui.Progress) error {
	return pull(ctx, source, cacheRoot, progress, false)
}

// PullIfMissing imports source unless the requested digest is cached and valid.
func PullIfMissing(ctx context.Context, source, digest, cacheRoot string, progress tui.Progress) error {
	ref, err := ParseSource(source)
	if err != nil {
		return err
	}
	st := store.Store{Root: cacheRoot}
	if err = validateCache(st.RepoPath(ref), source, progress); err != nil {
		return err
	}
	descriptor, findErr := st.Descriptor(ctx, ref)
	if findErr == nil && descriptor.Digest.String() == digest {
		if progress != nil {
			progress(tui.Event{Type: tui.Status, Message: "Validating cached image..."})
		}
	}
	if findErr == nil && descriptor.Digest.String() == digest && cacheComplete(st, ref) {
		if progress != nil {
			size, sizeErr := imageSize(st, ref)
			if sizeErr != nil {
				return sizeErr
			}
			progress(tui.Event{Type: tui.Cached, Name: source, Current: size, Total: size})
		}
		return nil
	}
	return pull(ctx, source, cacheRoot, progress, false)
}

// pull imports an image, optionally reusing a complete digest-validated cache.
func pull(ctx context.Context, source string, cacheRoot string, progress tui.Progress, reuse bool) error {
	ref, err := ParseSource(source)
	if err != nil {
		return err
	}
	st := store.Store{Root: cacheRoot}
	if err = validateCache(st.RepoPath(ref), source, progress); err != nil {
		return err
	}
	if reuse && cacheComplete(st, ref) {
		if progress != nil {
			size, sizeErr := imageSize(st, ref)
			if sizeErr != nil {
				return sizeErr
			}
			progress(tui.Event{Type: tui.Cached, Name: source, Current: size, Total: size})
		}
		return nil
	}
	if progress == nil {
		return st.Pull(ctx, ref)
	}
	progress(tui.Event{Type: tui.Started, Name: source})
	transport := &transfer.Transport{Base: remote.DefaultTransport, Name: source, Progress: progress}
	err = st.Pull(ctx, ref, remote.WithTransport(transport))
	current, total := transport.Bytes()
	event := tui.Event{Type: tui.Done, Name: source, Current: current, Total: total}
	if err != nil {
		event.Type = tui.Failed
		event.Err = err
	}
	progress(event)
	return err
}

// cacheComplete reports whether the selected OCI image and all children validate.
func cacheComplete(st store.Store, ref name.Tag) bool {
	descriptor, err := st.Descriptor(context.Background(), ref)
	if err != nil {
		return false
	}
	repository, err := layout.FromPath(st.RepoPath(ref))
	if err != nil {
		return false
	}
	root, err := repository.ImageIndex()
	if err != nil {
		return false
	}
	if descriptor.MediaType.IsIndex() {
		index, err := root.ImageIndex(descriptor.Digest)
		return err == nil && validate.Index(index) == nil
	}
	image, err := root.Image(descriptor.Digest)
	return err == nil && validate.Image(image) == nil
}

// validateCache removes corrupt OCI blobs so Pull fetches only those blobs again.
func validateCache(repository, name string, progress tui.Progress) error {
	directory := filepath.Join(repository, "blobs", "sha256")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) != sha256.Size*2 {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		total += info.Size()
	}
	if progress != nil && total > 0 {
		progress(tui.Event{Type: tui.Checking, Name: name, Total: total})
	}
	var current int64
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) != sha256.Size*2 {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		reader := io.Reader(file)
		if progress != nil {
			reader = &transfer.Reader{Reader: file, Name: name, Total: total, Offset: current, Progress: progress}
		}
		copied, copyErr := io.Copy(hash, reader)
		current += copied
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != entry.Name() {
			if err = os.Remove(path); err != nil {
				return err
			}
			continue
		}
	}
	return nil
}

// CacheSize returns the total content bytes stored for source in the local OCI cache.
func CacheSize(source, cacheRoot string) (int64, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return 0, err
	}
	return imageSize(store.Store{Root: cacheRoot}, ref)
}

// imageSize returns the unique content bytes reachable from ref.
func imageSize(st store.Store, ref name.Tag) (int64, error) {
	descriptor, err := st.Descriptor(context.Background(), ref)
	if err != nil {
		return 0, err
	}
	repository, err := layout.FromPath(st.RepoPath(ref))
	if err != nil {
		return 0, err
	}
	root, err := repository.ImageIndex()
	if err != nil {
		return 0, err
	}
	return descriptorSize(root, descriptor, map[v1.Hash]bool{})
}

// descriptorSize sums unique manifests, configuration, and layers below descriptor.
func descriptorSize(index v1.ImageIndex, descriptor v1.Descriptor, seen map[v1.Hash]bool) (int64, error) {
	if seen[descriptor.Digest] {
		return 0, nil
	}
	seen[descriptor.Digest] = true
	size := descriptor.Size
	if descriptor.MediaType.IsIndex() {
		childIndex, err := index.ImageIndex(descriptor.Digest)
		if err != nil {
			return 0, err
		}
		manifest, err := childIndex.IndexManifest()
		if err != nil {
			return 0, err
		}
		for _, child := range manifest.Manifests {
			childSize, err := descriptorSize(childIndex, child, seen)
			if err != nil {
				return 0, err
			}
			size += childSize
		}
		return size, nil
	}
	image, err := index.Image(descriptor.Digest)
	if err != nil {
		return 0, err
	}
	manifest, err := image.Manifest()
	if err != nil {
		return 0, err
	}
	for _, child := range append([]v1.Descriptor{manifest.Config}, manifest.Layers...) {
		if !seen[child.Digest] {
			seen[child.Digest] = true
			size += child.Size
		}
	}
	return size, nil
}
