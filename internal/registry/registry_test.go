// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
)

// TestAppNormalizesDestinations verifies default and shorthand application references.
func TestAppNormalizesDestinations(t *testing.T) {
	t.Parallel()
	source, err := name.NewTag("ghcr.io/acme/web:v1")
	if err != nil {
		t.Fatal(err)
	}
	for destination, expected := range map[string]string{
		"":            "registry.podmin.internal/apps/ghcr.io/acme/web:v1",
		"team/web:v2": "registry.podmin.internal/apps/team/web:v2",
		"web:v2":      "registry.podmin.internal/apps/web:v2",
	} {
		ref, appErr := App(source, destination)
		if appErr != nil {
			t.Fatal(appErr)
		}
		if ref.Name() != expected {
			t.Errorf("App(%q) = %q, want %q", destination, ref.Name(), expected)
		}
	}
}

// TestParseAcceptsClusterNamespaces verifies shorthand, app, and mirror references.
func TestParseAcceptsClusterNamespaces(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"hello":                                  "registry.podmin.internal/apps/hello:latest",
		"registry.podmin.internal/apps/hello:v1": "registry.podmin.internal/apps/hello:v1",
		"registry.podmin.internal/mirror/registry.k8s.io/pause:3.10": "registry.podmin.internal/mirror/registry.k8s.io/pause:3.10",
	} {
		ref, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Name() != expected {
			t.Errorf("Parse(%q) = %q, want %q", input, ref.Name(), expected)
		}
	}
}

// TestIsAppIgnoresTagsButNotRepositories verifies exact application identity matching.
func TestIsAppIgnoresTagsButNotRepositories(t *testing.T) {
	hello, err := Parse("hello:v1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Parse("other:v1")
	if err != nil {
		t.Fatal(err)
	}
	if !IsApp(hello, "hello") || IsApp(other, "hello") {
		t.Fatal("IsApp did not match the exact application repository")
	}
}

// TestParseRejectsExternalAndDigestReferences verifies cluster image boundaries.
func TestParseRejectsExternalAndDigestReferences(t *testing.T) {
	t.Parallel()
	for input, expected := range map[string]string{
		"ghcr.io/acme/hello:v1":                          "not in the cluster registry",
		"registry.podmin.internal/apps/hello@sha256:abc": "parse image reference",
	} {
		if _, err := Parse(input); err == nil || !strings.Contains(err.Error(), expected) {
			t.Errorf("Parse(%q) error = %v", input, err)
		}
	}
}

// TestMirrorConstructsClusterReference verifies source identity is retained beneath mirror.
func TestMirrorConstructsClusterReference(t *testing.T) {
	t.Parallel()
	source, err := name.NewTag("registry.k8s.io/pause:3.10")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := Mirror(source)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name() != "registry.podmin.internal/mirror/registry.k8s.io/pause:3.10" {
		t.Fatalf("Mirror() = %q", ref.Name())
	}
}
