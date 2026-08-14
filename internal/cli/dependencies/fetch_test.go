// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/tui"
)

// TestChecksum selects an exact filename and accepts single-value sidecars.
func TestChecksum(t *testing.T) {
	got, err := checksum([]byte("abc  other\ndef *artifact\n"), "artifact")
	if err != nil || got != "def" {
		t.Fatalf("checksum = %q, %v", got, err)
	}
	got, err = checksum([]byte("ABC\n"), "artifact")
	if err != nil || got != "abc" {
		t.Fatalf("single checksum = %q, %v", got, err)
	}
}

// TestTarGzip verifies deterministic executable wrapping for binary releases.
func TestTarGzip(t *testing.T) {
	first, err := tarGzip("zot", []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := tarGzip("zot", []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("archive is not deterministic")
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(tarReader)
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "zot" || header.Mode != 0755 || string(body) != "binary" {
		t.Fatalf("archive entry = %#v %q", header, body)
	}
}

// TestReadBounded accepts the exact limit and rejects an extra byte.
func TestReadBounded(t *testing.T) {
	body, err := readBounded(bytes.NewBufferString("abc"), 3)
	if err != nil || string(body) != "abc" {
		t.Fatalf("exact limit = %q, %v", body, err)
	}
	if _, err = readBounded(bytes.NewBufferString("abcd"), 3); err == nil {
		t.Fatal("oversized body was accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1073741825")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	fetcher := Fetcher{Client: server.Client()}
	if _, err = fetcher.get(context.Background(), server.URL, ""); err == nil {
		t.Fatal("oversized Content-Length was accepted")
	}
}

// TestGetReportsDownloadProgress verifies dependency bodies report their sizes.
func TestGetReportsDownloadProgress(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "artifact")
	}))
	defer server.Close()
	var events []tui.Event
	fetcher := Fetcher{Client: server.Client(), Progress: func(event tui.Event) { events = append(events, event) }}
	body, err := fetcher.get(context.Background(), server.URL, "artifact.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "artifact" || len(events) < 3 || events[0].Type != tui.Started || events[len(events)-1].Type != tui.Done || events[len(events)-1].Current != 8 {
		t.Fatalf("body = %q, events = %#v", body, events)
	}
}

// TestFetchReusesValidCacheAndRepairsCorruption verifies per-file cache validation.
func TestFetchReusesValidCacheAndRepairsCorruption(t *testing.T) {
	t.Parallel()
	body := []byte("artifact")
	sum := sha256.Sum256(body)
	var assetRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/artifact":
			assetRequests.Add(1)
			_, _ = writer.Write(body)
		case "/artifact.sha256":
			_, _ = fmt.Fprintf(writer, "%x  artifact\n", sum)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	dependency := Dependency{Key: "test", Architectures: map[string]string{"arm64": "arm64"}, AssetName: "artifact", AssetURL: server.URL + "/artifact", ChecksumURL: server.URL + "/artifact.sha256", ChecksumName: "artifact", ChecksumAlgorithm: "sha256", ObjectName: "artifact"}
	var events []tui.Event
	fetcher := Fetcher{Client: server.Client(), CacheDir: t.TempDir(), Progress: func(event tui.Event) { events = append(events, event) }}
	resolved, err := fetcher.resolveArtifact(context.Background(), dependency, "v1.0.0", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	first, err := fetcher.download(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fetcher.download(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if assetRequests.Load() != 1 {
		t.Fatalf("valid cache caused %d asset requests, want 1", assetRequests.Load())
	}
	if err = os.WriteFile(filepath.Clean(first.Path), []byte("corrupt!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = fetcher.download(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if assetRequests.Load() != 2 {
		t.Fatalf("corrupt cache caused %d asset requests, want 2", assetRequests.Load())
	}
	cached := false
	for _, event := range events {
		cached = cached || event.Type == tui.Cached
	}
	if !cached {
		t.Fatalf("events = %#v, want cached event", events)
	}
}

// TestCachedArtifactValidatesDigest verifies local artifacts bind their published digest.
func TestCachedArtifactValidatesDigest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact")
	body := []byte("artifact")
	sum, _, err := digest("sha512", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := "sha512:" + sum
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	size, cached := cachedArtifact(path, artifactDigest)
	if !cached || size != int64(len(body)) {
		t.Fatalf("cachedArtifact() = %d, %t", size, cached)
	}
	if _, cached = cachedArtifact(path, "sha512:"+strings.Repeat("0", 128)); cached {
		t.Fatal("artifact accepted a different digest")
	}
}
