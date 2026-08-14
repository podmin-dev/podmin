// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"strings"
	"testing"
)

// TestManifestRoundTrip verifies the public manifest shape and deterministic encoding.
func TestManifestRoundTrip(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	manifest := NewManifest(map[string][]Artifact{"arm64": {{Key: "zot", Version: "2.1.8", URL: "https://example.com/zot", ObjectKey: "dependencies/zot/zot-v2.1.8-linux-arm64", Digest: digest, Size: 42}}}, map[string]Image{"pause": {Version: "3.10.2", Source: "registry.k8s.io/pause:3.10.2", Path: "mirror/registry.k8s.io/pause", Digest: digest, Size: 84}})
	body, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"zot"`, `"arm64"`, `"url": "https://example.com/zot"`, `"path": "dependencies/zot/zot-v2.1.8-linux-arm64"`, `"pause"`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("manifest = %s, want %s", body, field)
		}
	}
	parsed, err := ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parsed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(body) {
		t.Fatalf("second encoding differs:\n%s\n%s", body, second)
	}
}

// TestManifestRejectsInvalidData verifies control data is strict and bounded by its schema.
func TestManifestRejectsInvalidData(t *testing.T) {
	for _, body := range []string{
		`{"version":2,"dependencies":{},"images":{}}`,
		`{"version":1,"dependencies":{},"images":{},"unknown":true}`,
		`{"version":1,"dependencies":{"zot":{"s390x":{"version":"1","url":"x","path":"dependencies/zot/zot","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1}}},"images":{}}`,
	} {
		if _, err := ParseManifest([]byte(body)); err == nil {
			t.Fatalf("ParseManifest(%s) succeeded", body)
		}
	}
}
