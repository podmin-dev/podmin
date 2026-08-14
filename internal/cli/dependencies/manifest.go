// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// ManifestPath is the authoritative dependency manifest object key.
const ManifestPath = "dependencies/manifest.json"

var manifestNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var manifestDigestPattern = regexp.MustCompile(`^sha(256:[0-9a-f]{64}|512:[0-9a-f]{128})$`)

// Manifest records the dependency files and images published for a cluster.
type Manifest struct {
	Version      int                        `json:"version"`
	Dependencies map[string]map[string]File `json:"dependencies"`
	Images       map[string]Image           `json:"images"`
}

// File records one architecture-specific dependency file.
type File struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
}

// Image records one setup-managed OCI image.
type Image struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
}

// NewManifest creates a manifest from resolved artifacts and images.
func NewManifest(artifacts map[string][]Artifact, images map[string]Image) Manifest {
	result := Manifest{Version: 1, Dependencies: map[string]map[string]File{}, Images: images}
	for architecture, files := range artifacts {
		for _, artifact := range files {
			if result.Dependencies[artifact.Key] == nil {
				result.Dependencies[artifact.Key] = map[string]File{}
			}
			result.Dependencies[artifact.Key][architecture] = File{Version: artifact.Version, URL: artifact.URL, Path: artifact.ObjectKey, Digest: artifact.Digest, Size: artifact.Size}
		}
	}
	return result
}

// ParseManifest parses and validates a dependency manifest.
func ParseManifest(body []byte) (Manifest, error) {
	var result Manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Manifest{}, fmt.Errorf("decode dependency manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("decode dependency manifest: trailing data")
	}
	if err := result.Validate(); err != nil {
		return Manifest{}, err
	}
	return result, nil
}

// Marshal validates and encodes a dependency manifest deterministically.
func (m Manifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// Validate checks the manifest version and every published object.
func (m Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported dependency manifest version %d", m.Version)
	}
	if m.Dependencies == nil || m.Images == nil {
		return errors.New("dependency manifest maps are required")
	}
	for name, architectures := range m.Dependencies {
		if !manifestNamePattern.MatchString(name) || len(architectures) == 0 {
			return fmt.Errorf("invalid dependency %q", name)
		}
		for architecture, file := range architectures {
			if architecture != "amd64" && architecture != "arm64" {
				return fmt.Errorf("invalid architecture %q for %s", architecture, name)
			}
			if file.Version == "" || file.URL == "" || !validObject(file.Path, "dependencies/") || !manifestDigestPattern.MatchString(file.Digest) || file.Size <= 0 {
				return fmt.Errorf("invalid dependency file %s/%s", name, architecture)
			}
		}
	}
	for name, image := range m.Images {
		if !manifestNamePattern.MatchString(name) || image.Version == "" || image.Source == "" || !validObject(image.Path, "mirror/") || !manifestDigestPattern.MatchString(image.Digest) || image.Size <= 0 {
			return fmt.Errorf("invalid dependency image %q", name)
		}
	}
	return nil
}

// SameFile reports whether a published file satisfies a resolved artifact.
func SameFile(file File, artifact Artifact) bool {
	return file.Version == artifact.Version && file.URL == artifact.URL && file.Path == artifact.ObjectKey && file.Digest == artifact.Digest && file.Size > 0
}

// SameImage reports whether two image records identify the same published image.
func SameImage(published, desired Image) bool {
	return published.Version == desired.Version && published.Source == desired.Source && published.Path == desired.Path && published.Digest == desired.Digest && published.Size > 0
}

// validObject checks that value is a clean object-storage path below prefix.
func validObject(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && path.Clean(value) == value && !strings.ContainsAny(value, `*?[]\`)
}
