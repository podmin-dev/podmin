// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/podplane/ocimage/pkg/store"
)

// Push uploads a cached OCI repository with immutable objects first and returns
// the normalized cluster image reference.
func Push(ctx context.Context, source, destination, cacheRoot string, refresh bool, objects ObjectStore) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	st := store.Store{Root: cacheRoot}
	if refresh {
		err = Pull(ctx, source, cacheRoot)
	} else if _, findErr := st.Descriptor(ctx, ref); findErr != nil {
		err = Pull(ctx, source, cacheRoot)
	}
	if err != nil {
		return "", err
	}
	dst, prefix, err := NormalizeDestination(ref, destination)
	if err != nil {
		return "", err
	}
	repo := st.RepoPath(ref)
	if err = uploadTree(ctx, objects, repo, prefix); err != nil {
		return "", err
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

// Mirror publishes an upstream image beneath the setup-managed mirror namespace.
func Mirror(ctx context.Context, source, cacheRoot string, objects ObjectStore) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	if err = Pull(ctx, source, cacheRoot); err != nil {
		return "", err
	}
	st := store.Store{Root: cacheRoot}
	prefix := "mirror/" + ref.RegistryStr() + "/" + ref.Context().RepositoryStr()
	if err = uploadTree(ctx, objects, st.RepoPath(ref), prefix); err != nil {
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

// uploadTree uploads immutable blobs first and oci-layout second, excluding index.json.
func uploadTree(ctx context.Context, objects ObjectStore, repo, prefix string) error {
	for _, root := range []string{"blobs", "oci-layout"} {
		path := filepath.Join(repo, root)
		err := filepath.WalkDir(path, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repo, file)
			if err != nil {
				return err
			}
			return objects.Put(ctx, prefix+"/"+filepath.ToSlash(rel), body)
		})
		if err != nil {
			return err
		}
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
