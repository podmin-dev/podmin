// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/podmin-dev/podmin/internal/cli/transfer"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podplane/ocimage/pkg/store"
)

// Push uploads a cached OCI repository with immutable objects first and returns
// the normalized cluster image reference.
func Push(ctx context.Context, source, destination, cacheRoot string, refresh bool, objects ObjectStore, progress tui.Progress) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	st := store.Store{Root: cacheRoot}
	if refresh {
		err = Pull(ctx, source, cacheRoot, progress)
	} else if _, findErr := st.Descriptor(ctx, ref); findErr != nil {
		err = Pull(ctx, source, cacheRoot, progress)
	}
	if err != nil {
		return "", err
	}
	dst, prefix, err := NormalizeDestination(ref, destination)
	if err != nil {
		return "", err
	}
	repo := st.RepoPath(ref)
	if err = uploadTree(ctx, objects, repo, prefix, dst.Name(), progress); err != nil {
		return "", err
	}
	if progress != nil {
		progress(tui.Event{Type: tui.Status, Message: "Publishing image index..."})
	}
	localIndex, err := os.ReadFile(filepath.Join(repo, "index.json"))
	if err != nil {
		return "", err
	}
	key := prefix + "/index.json"
	current, version, readErr := objects.Get(ctx, key)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	merged, err := mergeIndexes(current, localIndex, ref.TagStr(), dst.TagStr())
	if err != nil {
		return "", err
	}
	if err = objects.PutIfMatch(ctx, key, merged, version); err != nil {
		return "", fmt.Errorf("publish image index: %w", err)
	}
	return dst.Name(), nil
}

// Mirror publishes a cached image beneath the setup-managed mirror namespace.
func Mirror(ctx context.Context, source, cacheRoot string, objects ObjectStore, progress tui.Progress) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	st := store.Store{Root: cacheRoot}
	if _, err = st.Descriptor(ctx, ref); err != nil {
		return "", fmt.Errorf("find cached image: %w", err)
	}
	prefix := mirrorPath(ref)
	if err = uploadTree(ctx, objects, st.RepoPath(ref), prefix, source, progress); err != nil {
		return "", err
	}
	localIndex, err := os.ReadFile(filepath.Join(st.RepoPath(ref), "index.json"))
	if err != nil {
		return "", err
	}
	key := prefix + "/index.json"
	current, version, readErr := objects.Get(ctx, key)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	merged, err := mergeIndexes(current, localIndex, ref.TagStr(), ref.TagStr())
	if err != nil {
		return "", err
	}
	if err = objects.PutIfMatch(ctx, key, merged, version); err != nil {
		return "", err
	}
	return "registry.podmin.internal/" + prefix + ":" + ref.TagStr(), nil
}

// MirrorPath returns the object-storage repository path for source.
func MirrorPath(source string) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	return mirrorPath(ref), nil
}

// mirrorPath returns the setup-managed repository path for ref.
func mirrorPath(ref name.Tag) string {
	return "mirror/" + ref.RegistryStr() + "/" + ref.Context().RepositoryStr()
}

// uploadTree uploads immutable blobs first and oci-layout second, excluding index.json.
func uploadTree(ctx context.Context, objects ObjectStore, repo, prefix, name string, progress tui.Progress) (err error) {
	if progress != nil {
		defer func() {
			if err != nil {
				progress(tui.Event{Type: tui.Failed, Name: name, Err: err})
			}
		}()
	}
	var total int64
	for _, root := range []string{"blobs", "oci-layout"} {
		err := filepath.WalkDir(filepath.Join(repo, root), func(_ string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			return err
		}
	}
	if progress != nil {
		progress(tui.Event{Type: tui.Started, Name: name, Total: total})
	}
	var current int64
	for _, root := range []string{"blobs", "oci-layout"} {
		path := filepath.Join(repo, root)
		err := filepath.WalkDir(path, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repo, file)
			if err != nil {
				return err
			}
			body, err := os.Open(file)
			if err != nil {
				return err
			}
			info, err := body.Stat()
			if err != nil {
				_ = body.Close()
				return err
			}
			reader := io.Reader(body)
			if progress != nil {
				reader = &transfer.Reader{Reader: body, Name: name, Total: total, Offset: current, Progress: progress}
			}
			uploadErr := objects.PutStream(ctx, prefix+"/"+filepath.ToSlash(rel), reader, info.Size())
			closeErr := body.Close()
			if uploadErr != nil {
				return uploadErr
			}
			current += info.Size()
			return closeErr
		})
		if err != nil {
			return err
		}
	}
	if progress != nil {
		progress(tui.Event{Type: tui.Done, Name: name, Current: current, Total: total})
	}
	return nil
}

// mergeIndexes replaces the destination tag while preserving all other tags.
func mergeIndexes(current, local []byte, sourceTag, destinationTag string) ([]byte, error) {
	var incoming, existing v1.Index
	if err := json.Unmarshal(local, &incoming); err != nil {
		return nil, fmt.Errorf("read local OCI index: %w", err)
	}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &existing); err != nil {
			return nil, fmt.Errorf("read remote OCI index: %w", err)
		}
	} else {
		existing.SchemaVersion = 2
	}
	kept := existing.Manifests[:0]
	for _, descriptor := range existing.Manifests {
		if descriptor.Annotations[v1.AnnotationRefName] != destinationTag {
			kept = append(kept, descriptor)
		}
	}
	added := false
	for _, descriptor := range incoming.Manifests {
		if descriptor.Annotations[v1.AnnotationRefName] == sourceTag {
			if descriptor.Annotations == nil {
				descriptor.Annotations = map[string]string{}
			}
			descriptor.Annotations[v1.AnnotationRefName] = destinationTag
			kept = append(kept, descriptor)
			added = true
		}
	}
	if !added {
		return nil, fmt.Errorf("source tag %q is absent from the local OCI index", sourceTag)
	}
	existing.Manifests = kept
	return json.Marshal(existing)
}
