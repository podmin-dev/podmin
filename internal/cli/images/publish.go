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
	"github.com/podmin-dev/podmin/internal/registry"
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
	dst, err := registry.App(ref, destination)
	if err != nil {
		return "", err
	}
	prefix := dst.Context().RepositoryStr()
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
	merged, err := mergeIndexes(repo, current, localIndex, ref.TagStr(), dst.TagStr())
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
	dst, err := registry.Mirror(ref)
	if err != nil {
		return "", err
	}
	prefix := dst.Context().RepositoryStr()
	repo := st.RepoPath(ref)
	if err = uploadTree(ctx, objects, repo, prefix, source, progress); err != nil {
		return "", err
	}
	return publishMirrorIndex(ctx, ref, dst, repo, objects)
}

// PublishMirrorIndex repairs mirror metadata from a complete local cache without
// uploading immutable blobs again.
func PublishMirrorIndex(ctx context.Context, source, cacheRoot string, objects ObjectStore) (string, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", err
	}
	st := store.Store{Root: cacheRoot}
	if _, err = st.Descriptor(ctx, ref); err != nil {
		return "", fmt.Errorf("find cached image: %w", err)
	}
	dst, err := registry.Mirror(ref)
	if err != nil {
		return "", err
	}
	return publishMirrorIndex(ctx, ref, dst, st.RepoPath(ref), objects)
}

// publishMirrorIndex conditionally merges one mirror tag and its child manifests.
func publishMirrorIndex(ctx context.Context, ref, dst name.Tag, repo string, objects ObjectStore) (string, error) {
	localIndex, err := os.ReadFile(filepath.Join(repo, "index.json"))
	if err != nil {
		return "", err
	}
	key := dst.Context().RepositoryStr() + "/index.json"
	current, version, readErr := objects.Get(ctx, key)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	merged, err := mergeIndexes(repo, current, localIndex, ref.TagStr(), ref.TagStr())
	if err != nil {
		return "", err
	}
	if err = objects.PutIfMatch(ctx, key, merged, version); err != nil {
		return "", err
	}
	return dst.Name(), nil
}

// Mirrored reports whether the expected source digest is already published in the cluster mirror.
func Mirrored(ctx context.Context, source, digest string, objects ObjectStore) (string, bool, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return "", false, err
	}
	dst, err := registry.Mirror(ref)
	if err != nil {
		return "", false, err
	}
	body, _, err := objects.Get(ctx, dst.Context().RepositoryStr()+"/index.json")
	if errors.Is(err, os.ErrNotExist) {
		return dst.Name(), false, nil
	}
	if err != nil {
		return "", false, err
	}
	var index v1.Index
	if err = json.Unmarshal(body, &index); err != nil {
		return "", false, fmt.Errorf("read mirrored image index: %w", err)
	}
	published := make(map[string]bool, len(index.Manifests))
	for _, descriptor := range index.Manifests {
		published[descriptor.Digest.String()] = true
	}
	for _, descriptor := range index.Manifests {
		if descriptor.Annotations[v1.AnnotationRefName] == ref.TagStr() {
			if descriptor.Digest.String() != digest {
				return dst.Name(), false, nil
			}
			complete, completeErr := publishedIndexComplete(ctx, dst.Context().RepositoryStr(), descriptor, published, objects)
			return dst.Name(), complete, completeErr
		}
	}
	return dst.Name(), false, nil
}

// publishedIndexComplete verifies Zot can resolve every nested manifest by digest.
func publishedIndexComplete(ctx context.Context, prefix string, descriptor v1.Descriptor, published map[string]bool, objects ObjectStore) (bool, error) {
	if !imageIndexMediaType(descriptor.MediaType) {
		return true, nil
	}
	key := prefix + "/blobs/" + descriptor.Digest.Algorithm().String() + "/" + descriptor.Digest.Encoded()
	body, _, err := objects.Get(ctx, key)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var index v1.Index
	if err = json.Unmarshal(body, &index); err != nil {
		return false, fmt.Errorf("read mirrored image index %s: %w", descriptor.Digest, err)
	}
	for _, child := range index.Manifests {
		if !published[child.Digest.String()] {
			return false, nil
		}
		complete, completeErr := publishedIndexComplete(ctx, prefix, child, published, objects)
		if completeErr != nil || !complete {
			return complete, completeErr
		}
	}
	return true, nil
}

// UploadSize returns the bytes uploaded for source's cached OCI repository.
func UploadSize(source, cacheRoot string) (int64, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return 0, err
	}
	return treeSize(store.Store{Root: cacheRoot}.RepoPath(ref))
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
	total, err := treeSize(repo)
	if err != nil {
		return err
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

// treeSize returns the bytes uploaded from one cached OCI repository.
func treeSize(repo string) (int64, error) {
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
			return 0, err
		}
	}
	return total, nil
}

// mergeIndexes replaces the destination tag, adds Zot-visible child manifests,
// and preserves all other descriptors.
func mergeIndexes(repo string, current, local []byte, sourceTag, destinationTag string) ([]byte, error) {
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
	seen := make(map[string]bool, len(kept))
	for _, descriptor := range kept {
		seen[descriptor.Digest.String()] = true
	}
	added := false
	for _, descriptor := range incoming.Manifests {
		if descriptor.Annotations[v1.AnnotationRefName] == sourceTag {
			if descriptor.Annotations == nil {
				descriptor.Annotations = map[string]string{}
			}
			descriptor.Annotations[v1.AnnotationRefName] = destinationTag
			kept = append(kept, descriptor)
			seen[descriptor.Digest.String()] = true
			if err := appendIndexChildren(repo, &kept, descriptor, seen); err != nil {
				return nil, err
			}
			added = true
		}
	}
	if !added {
		return nil, fmt.Errorf("source tag %q is absent from the local OCI index", sourceTag)
	}
	existing.Manifests = kept
	return json.Marshal(existing)
}

// appendIndexChildren adds every nested manifest descriptor required for Zot's
// digest-based manifest lookup.
func appendIndexChildren(repo string, target *[]v1.Descriptor, descriptor v1.Descriptor, seen map[string]bool) error {
	if !imageIndexMediaType(descriptor.MediaType) {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(repo, "blobs", descriptor.Digest.Algorithm().String(), descriptor.Digest.Encoded()))
	if err != nil {
		return fmt.Errorf("read image index %s: %w", descriptor.Digest, err)
	}
	var index v1.Index
	if err = json.Unmarshal(body, &index); err != nil {
		return fmt.Errorf("decode image index %s: %w", descriptor.Digest, err)
	}
	for _, child := range index.Manifests {
		if !seen[child.Digest.String()] {
			*target = append(*target, child)
			seen[child.Digest.String()] = true
		}
		if err = appendIndexChildren(repo, target, child, seen); err != nil {
			return err
		}
	}
	return nil
}

// imageIndexMediaType reports whether a descriptor contains nested manifests.
func imageIndexMediaType(mediaType string) bool {
	return mediaType == v1.MediaTypeImageIndex || mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}
