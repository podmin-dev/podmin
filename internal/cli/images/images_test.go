// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podplane/ocimage/pkg/store"
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

// put records an unconditional in-memory object write.
func (f *fakeStore) put(key string, body []byte) error {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	f.objects[key] = body
	f.order = append(f.order, key)
	return nil
}

// PutStream records a streamed in-memory object write.
func (f *fakeStore) PutStream(_ context.Context, key string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return f.put(key, data)
}

// PutIfMatch records a conditional in-memory object write.
func (f *fakeStore) PutIfMatch(_ context.Context, key string, body []byte, _ string) error {
	return f.put(key, body)
}

// TestUploadTreePublishesIndexLast verifies registry-safe object publication order.
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
	var events []tui.Event
	if err := uploadTree(context.Background(), fake, dir, "apps/test", "test", func(event tui.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.order, []string{"apps/test/blobs/sha256/abc", "apps/test/oci-layout"}) {
		t.Fatalf("upload order = %v", fake.order)
	}
	if _, _, err := fake.Get(context.Background(), "apps/test/index.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("index.json was uploaded early")
	}
	if len(events) < 2 || events[0].Type != tui.Started || events[len(events)-1].Type != tui.Done || events[len(events)-1].Current != 10 {
		t.Fatalf("upload events = %#v", events)
	}
}

// TestPushReportsUploadAndIndexPublication verifies push accounts for work after image preparation.
func TestPushReportsUploadAndIndexPublication(t *testing.T) {
	t.Parallel()
	ref, err := name.NewTag("registry.example/test/image:v1")
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	st := store.Store{Root: cache}
	if err = st.PutImage(context.Background(), ref, empty.Image); err != nil {
		t.Fatal(err)
	}
	uploadSize, err := UploadSize(ref.Name(), cache)
	if err != nil || uploadSize <= 0 {
		t.Fatalf("UploadSize() = %d, %v", uploadSize, err)
	}
	fake := &fakeStore{}
	var events []tui.Event
	destination, err := Push(context.Background(), ref.Name(), "", cache, false, fake, func(event tui.Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if destination != "registry.podmin.internal/apps/registry.example/test/image:v1" {
		t.Fatalf("Push() = %q", destination)
	}
	uploaded, published := -1, -1
	var reportedSize int64
	for i, event := range events {
		if event.Type == tui.Started {
			reportedSize = event.Total
		}
		if event.Type == tui.Done {
			uploaded = i
		}
		if event.Type == tui.Status && event.Message == "Publishing image index..." {
			published = i
		}
	}
	if uploaded < 0 || published <= uploaded || reportedSize != uploadSize {
		t.Fatalf("push events = %#v", events)
	}
}

// TestMirrorPublishesIndexChildren verifies the registry can resolve platform manifests by digest.
func TestMirrorPublishesIndexChildren(t *testing.T) {
	t.Parallel()
	ref, err := name.NewTag("registry.example/pause:v1")
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	st := store.Store{Root: cache}
	index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: empty.Image})
	if err = st.PutIndex(context.Background(), ref, index); err != nil {
		t.Fatal(err)
	}
	fake := &fakeStore{}
	if _, err = Mirror(context.Background(), ref.Name(), cache, fake, nil); err != nil {
		t.Fatal(err)
	}
	body := fake.objects["mirror/registry.example/pause/index.json"]
	var published struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err = json.Unmarshal(body, &published); err != nil {
		t.Fatal(err)
	}
	if len(published.Manifests) != 2 || published.Manifests[0].Annotations["org.opencontainers.image.ref.name"] != "v1" || published.Manifests[1].Digest == "" {
		t.Fatalf("published index = %s", body)
	}
}

// TestMirroredFindsOnlyTheExpectedTagDigest verifies install-time mirror reuse is exact.
func TestMirroredFindsOnlyTheExpectedTagDigest(t *testing.T) {
	t.Parallel()
	const source = "docker.io/cloudflare/cloudflared:2026.8.2"
	const digest = "sha256:0aa26e284f05e6c77ae375b8c9c11d9eb6a448fb7bcd8d40f31cb6176189eb38"
	fake := &fakeStore{objects: map[string][]byte{}}
	ref, found, err := Mirrored(context.Background(), source, digest, fake)
	if err != nil || found || ref != "registry.podmin.internal/mirror/index.docker.io/cloudflare/cloudflared:2026.8.2" {
		t.Fatalf("missing Mirrored() = %q, %t, %v", ref, found, err)
	}
	fake.objects["mirror/index.docker.io/cloudflare/cloudflared/index.json"] = []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + digest + `","size":1,"annotations":{"org.opencontainers.image.ref.name":"2026.8.2"}}]}`)
	_, found, err = Mirrored(context.Background(), source, digest, fake)
	if err != nil || !found {
		t.Fatalf("matching Mirrored() = %t, %v", found, err)
	}
	_, found, err = Mirrored(context.Background(), source, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fake)
	if err != nil || found {
		t.Fatalf("stale Mirrored() = %t, %v", found, err)
	}
}

// TestMirroredRequiresRegistryVisibleChildren verifies incomplete legacy indexes are republished.
func TestMirroredRequiresRegistryVisibleChildren(t *testing.T) {
	t.Parallel()
	const source = "registry.example/pause:v1"
	const root = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const child = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	prefix := "mirror/registry.example/pause/"
	index := `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"` + root + `","size":1,"annotations":{"org.opencontainers.image.ref.name":"v1"}}]}`
	rootBody := `{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + child + `","size":1}]}`
	fake := &fakeStore{objects: map[string][]byte{prefix + "index.json": []byte(index), prefix + "blobs/sha256/" + strings.TrimPrefix(root, "sha256:"): []byte(rootBody)}}
	_, found, err := Mirrored(context.Background(), source, root, fake)
	if err != nil || found {
		t.Fatalf("hidden child Mirrored() = %t, %v", found, err)
	}
	fake.objects[prefix+"index.json"] = []byte(strings.Replace(index, `]}`, `,{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+child+`","size":1}]}`, 1))
	_, found, err = Mirrored(context.Background(), source, root, fake)
	if err != nil || !found {
		t.Fatalf("published child Mirrored() = %t, %v", found, err)
	}
}

// TestPullReportsRegistryDownloads verifies image manifests and blobs emit progress.
func TestPullReportsRegistryDownloads(t *testing.T) {
	t.Parallel()
	handler := registry.New()
	var blobRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/blobs/") && request.Method == http.MethodGet {
			blobRequests.Add(1)
		}
		handler.ServeHTTP(writer, request)
	}))
	defer server.Close()
	ref, err := name.NewTag(strings.TrimPrefix(server.URL, "http://")+"/test/image:v1", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err = remote.Write(ref, empty.Image); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	var events []tui.Event
	if err = Pull(context.Background(), ref.Name(), cache, func(event tui.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	var started, done bool
	for _, event := range events {
		started = started || event.Type == tui.Started
		done = done || event.Type == tui.Done
	}
	if !started || !done {
		t.Fatalf("download events = %#v", events)
	}
	firstRequests := blobRequests.Load()
	st := store.Store{Root: cache}
	descriptor, err := st.Descriptor(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	if err = PullIfMissing(context.Background(), ref.Name(), descriptor.Digest.String(), cache, func(event tui.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	var checking bool
	for _, event := range events {
		checking = checking || event.Type == tui.Checking
	}
	if !checking {
		t.Fatalf("cache validation events = %#v", events)
	}
	if blobRequests.Load() != firstRequests {
		t.Fatalf("valid cache downloaded %d additional blobs", blobRequests.Load()-firstRequests)
	}
	if size, sizeErr := CacheSize(ref.Name(), cache); sizeErr != nil || size <= 0 {
		t.Fatalf("CacheSize() = %d, %v", size, sizeErr)
	}
	configDigest, err := empty.Image.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(st.RepoPath(ref), "blobs", "sha256", configDigest.Hex)
	corrupt, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	corrupt[0] ^= 0xff
	if err = os.WriteFile(blob, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err = pull(context.Background(), ref.Name(), cache, func(tui.Event) {}, true); err != nil {
		t.Fatal(err)
	}
	if blobRequests.Load() != firstRequests+1 {
		t.Fatalf("corrupt cache downloaded %d additional blobs, want 1", blobRequests.Load()-firstRequests)
	}
	repaired, err := os.ReadFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(repaired)
	if fmt.Sprintf("%x", sum) != filepath.Base(blob) {
		t.Fatal("corrupt OCI blob was not repaired")
	}
}

// TestCacheSizeTraversesImageIndexes verifies multi-platform children use their containing index.
func TestCacheSizeTraversesImageIndexes(t *testing.T) {
	t.Parallel()
	ref, err := name.NewTag("registry.example/pause:v1")
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	st := store.Store{Root: cache}
	index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: empty.Image})
	if err = st.PutIndex(context.Background(), ref, index); err != nil {
		t.Fatal(err)
	}
	if size, sizeErr := CacheSize(ref.Name(), cache); sizeErr != nil || size <= 0 {
		t.Fatalf("CacheSize() = %d, %v", size, sizeErr)
	}
}
