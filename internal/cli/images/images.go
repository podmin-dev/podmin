// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/podmin-dev/podmin/internal/cli/config"
)

// ObjectStore is the object-storage contract required by image publication.
// Get returns an opaque version used to replace mutable index.json objects.
type ObjectStore interface {
	Get(context.Context, string) ([]byte, string, error)
	PutStream(context.Context, string, io.Reader, int64) error
	PutIfMatch(context.Context, string, []byte, string) error
}

// CacheRoot returns Podmin's XDG-compatible OCI cache root.
func CacheRoot() (string, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oci"), nil
}

// ParseSource parses a tagged source reference.
func ParseSource(source string) (name.Tag, error) {
	ref, err := name.NewTag(strings.TrimSpace(source), name.WeakValidation)
	if err != nil {
		return name.Tag{}, fmt.Errorf("parse image source: %w", err)
	}
	if strings.Contains(source, "@") {
		return name.Tag{}, errors.New("image source must use a tag, not a digest")
	}
	return ref, nil
}

// NormalizeDestination returns the cluster reference and repository object prefix.
func NormalizeDestination(source name.Tag, destination string) (name.Tag, string, error) {
	value := strings.TrimSpace(destination)
	if value == "" {
		value = "registry.podmin.internal/apps/" + source.RegistryStr() + "/" + source.Context().RepositoryStr() + ":" + source.TagStr()
	} else if first, _, slash := strings.Cut(value, "/"); !slash || !strings.Contains(first, ".") && !strings.Contains(first, ":") {
		value = "registry.podmin.internal/apps/" + strings.TrimPrefix(value, "apps/")
	}
	dst, err := name.NewTag(value, name.WeakValidation)
	if err != nil {
		return name.Tag{}, "", fmt.Errorf("parse image destination: %w", err)
	}
	if dst.RegistryStr() != "registry.podmin.internal" || !strings.HasPrefix(dst.Context().RepositoryStr(), "apps/") {
		return name.Tag{}, "", errors.New("destination must be under registry.podmin.internal/apps/")
	}
	return dst, dst.Context().RepositoryStr(), nil
}
