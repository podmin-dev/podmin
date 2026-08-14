// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/dependencies"
)

// TestParseNodeGroups validates defaults, duplicates, and malformed names.
func TestParseNodeGroups(t *testing.T) {
	nodeGroups, err := parseNodeGroups([]string{"workers", "api,size=2,instance-type=m7g.large"})
	if err != nil {
		t.Fatal(err)
	}
	if nodeGroups["workers"].Size != 1 || nodeGroups["api"].Size != 2 || nodeGroups["api"].InstanceType != "m7g.large" {
		t.Fatalf("NodeGroups = %#v", nodeGroups)
	}
	for _, values := range [][]string{nil, {"workers", "workers"}, {"Not Valid"}} {
		if _, err = parseNodeGroups(values); err == nil {
			t.Fatalf("parseNodeGroups(%q) succeeded", values)
		}
	}
}

// TestPendingDependenciesSelectsOnlyUnpublishedArtifacts verifies warm buckets need no local cache.
func TestPendingDependenciesSelectsOnlyUnpublishedArtifacts(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	files := map[string][]dependencies.Artifact{"arm64": {
		{Key: "containerd", Version: "2.1.4", Architecture: "arm64", URL: "https://example.com/containerd", ObjectKey: "dependencies/containerd/containerd-v2.1.4-linux-arm64.tar.gz", Digest: digest},
		{Key: "zot", Version: "2.1.8", Architecture: "arm64", URL: "https://example.com/zot", ObjectKey: "dependencies/zot/zot-v2.1.8-linux-arm64", Digest: digest},
	}}
	published := dependencies.Manifest{Version: 1, Dependencies: map[string]map[string]dependencies.File{
		"containerd": {"arm64": {Version: "2.1.4", URL: "https://example.com/containerd", Path: files["arm64"][0].ObjectKey, Digest: digest, Size: 10}},
		"zot":        {"arm64": {Version: "2.1.7", URL: "https://example.com/zot", Path: files["arm64"][1].ObjectKey, Digest: digest, Size: 9}},
	}, Images: map[string]dependencies.Image{"pause": {Version: "3.10.2", Source: pauseImage, Path: "mirror/registry.k8s.io/pause", Digest: digest, Size: 8}}}
	image := dependencies.Image{Version: "3.10.2", Source: pauseImage, Path: "mirror/registry.k8s.io/pause", Digest: digest}
	pending, imagePending := pendingDependencies(published, files, &image)
	if imagePending || image.Size != 8 || files["arm64"][0].Size != 10 {
		t.Fatalf("published entries were not reused: imagePending=%t image=%#v files=%#v", imagePending, image, files)
	}
	if len(pending["arm64"]) != 1 || pending["arm64"][0].Key != "zot" {
		t.Fatalf("pending = %#v, want only zot", pending)
	}
}
