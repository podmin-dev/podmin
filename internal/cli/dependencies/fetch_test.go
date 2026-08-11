// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
	if _, err = fetcher.get(context.Background(), server.URL); err == nil {
		t.Fatal("oversized Content-Length was accepted")
	}
}
