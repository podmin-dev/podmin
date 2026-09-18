// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
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
	first, err := tarGzip("agent", []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := tarGzip("agent", []byte("binary"))
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
	if header.Name != "agent" || header.Mode != 0755 || string(body) != "binary" {
		t.Fatalf("archive entry = %#v %q", header, body)
	}
}
