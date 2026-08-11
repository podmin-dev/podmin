// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
)

// fakeStore records object operations and implements conditional replacement.
type fakeStore struct {
	objects map[string][]byte
	order   []string
}

// Get returns an in-memory object and synthetic version.
func (f *fakeStore) Get(_ context.Context, key string) ([]byte, string, error) {
	body, ok := f.objects[key]
	if !ok {
		return nil, "", os.ErrNotExist
	}
	return body, "version", nil
}

// Put records an unconditional in-memory object write.
func (f *fakeStore) Put(_ context.Context, key string, body []byte) error {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[key] = body
	f.order = append(f.order, key)
	return nil
}

// PutIfMatch records a conditional in-memory object write.
func (f *fakeStore) PutIfMatch(ctx context.Context, key string, body []byte, _ string) error {
	return f.Put(ctx, key, body)
}

// TestNormalizeDestination verifies default and shorthand cluster references.
func TestNormalizeDestination(t *testing.T) {
	source, err := name.NewTag("ghcr.io/acme/web:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, prefix, err := NormalizeDestination(source, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name() != "registry.podmin.internal/apps/ghcr.io/acme/web:v1" || prefix != "apps/ghcr.io/acme/web" {
		t.Fatalf("default destination = %q, prefix = %q", got.Name(), prefix)
	}
	got, _, err = NormalizeDestination(source, "team/web:v2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name() != "registry.podmin.internal/apps/team/web:v2" {
		t.Fatalf("shorthand destination = %q", got.Name())
	}
}

// TestUploadTreePublishesIndexLast verifies Zot-safe object publication order.
func TestUploadTreePublishesIndexLast(t *testing.T) {
	dir := t.TempDir()
	for path, body := range map[string]string{"blobs/sha256/abc": "blob", "oci-layout": "layout", "index.json": `{"schemaVersion":2,"manifests":[]}`} {
		full := dir + "/" + path
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeStore{}
	if err := uploadTree(context.Background(), fake, dir, "apps/test"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.order, []string{"apps/test/blobs/sha256/abc", "apps/test/oci-layout"}) {
		t.Fatalf("upload order = %v", fake.order)
	}
	if _, _, err := fake.Get(context.Background(), "apps/test/index.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("index.json was uploaded early")
	}
}
