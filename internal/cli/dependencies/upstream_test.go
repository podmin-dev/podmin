// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
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

// TestResolveCompressedAPTPackage derives a package URL and digest from a compressed index.
func TestResolveCompressedAPTPackage(t *testing.T) {
	digest := strings.Repeat("c", 64)
	index := "Package: libpq5\nArchitecture: arm64\nVersion: 17.11-0+deb13u1\nFilename: pool/main/p/postgresql-17/libpq5_17.11-0+deb13u1_arm64.deb\nSHA256: " + digest + "\n"
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(index)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(compressed.Bytes())
	}))
	defer server.Close()
	dependency := Dependency{
		Key:               "libpq5",
		Source:            aptRepositorySource,
		PackageIndex:      server.URL + "/Packages.gz",
		Architectures:     map[string]string{"arm64": "arm64"},
		AssetURL:          "https://deb.debian.org/debian/{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "libpq5.deb",
	}
	artifact, err := (Fetcher{Client: server.Client(), CacheDir: "/cache"}).resolveArtifact(context.Background(), dependency, "repository", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Version != "17.11-0+deb13u1" || artifact.URL != "https://deb.debian.org/debian/pool/main/p/postgresql-17/libpq5_17.11-0+deb13u1_arm64.deb" || artifact.Digest != "sha256:"+digest || artifact.Name != "libpq5.deb" {
		t.Fatalf("resolved libpq5 artifact = %#v", artifact)
	}
}

// TestResolveAPTPackageSelectsNewestConstrainedVersion verifies APT version and architecture selection.
func TestResolveAPTPackageSelectsNewestConstrainedVersion(t *testing.T) {
	digest := strings.Repeat("d", 128)
	index := "Package: fluent-bit\nArchitecture: amd64\nVersion: 5.1.1\nFilename: pool/fluent-bit_5.1.1_amd64.deb\nSHA512: " + strings.Repeat("a", 128) + "\n\n" +
		"Package: fluent-bit\nArchitecture: arm64\nVersion: 5.1.3\nFilename: pool/fluent-bit_5.1.3_arm64.deb\nSHA512: " + digest + "\n\n" +
		"Package: fluent-bit\nArchitecture: arm64\nVersion: 6.0.0\nFilename: pool/fluent-bit_6.0.0_arm64.deb\nSHA512: " + strings.Repeat("e", 128) + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(index))
	}))
	defer server.Close()
	dependency := Dependency{
		Key:               "fluent-bit",
		Major:             5,
		Source:            aptRepositorySource,
		PackageIndex:      server.URL + "/Packages",
		Architectures:     map[string]string{"arm64": "arm64"},
		AssetURL:          "https://packages.fluentbit.io/debian/trixie/{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "fluent-bit.deb",
	}
	artifact, err := (Fetcher{Client: server.Client(), CacheDir: "/cache"}).resolveArtifact(context.Background(), dependency, "repository", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Version != "5.1.3" || artifact.URL != "https://packages.fluentbit.io/debian/trixie/pool/fluent-bit_5.1.3_arm64.deb" || artifact.Digest != "sha512:"+digest || artifact.ObjectKey != "dependencies/fluent-bit/fluent-bit-v5.1.3-linux-arm64.deb" {
		t.Fatalf("resolved Fluent Bit artifact = %#v", artifact)
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
}

// TestGetRejectsOversizedContentLength verifies declared oversized responses fail before reading.
func TestGetRejectsOversizedContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1073741825")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	fetcher := Fetcher{Client: server.Client()}
	if _, err := fetcher.get(context.Background(), server.URL, ""); err == nil {
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

// TestDownloadReusesValidCacheAndRepairsCorruption verifies per-file cache validation.
func TestDownloadReusesValidCacheAndRepairsCorruption(t *testing.T) {
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
