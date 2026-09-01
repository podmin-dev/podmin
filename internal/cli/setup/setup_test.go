// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cli/tui"
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
		{Key: "coredns", Version: "1.13.1", Architecture: "arm64", URL: "https://example.com/coredns", ObjectKey: "dependencies/coredns/coredns-v1.13.1-linux-arm64.tar.gz", Digest: digest},
	}}
	published := dependencies.Manifest{Version: 1, Dependencies: map[string]map[string]dependencies.File{
		"containerd": {"arm64": {Version: "2.1.4", URL: "https://example.com/containerd", Path: files["arm64"][0].ObjectKey, Digest: digest, Size: 10}},
		"coredns":    {"arm64": {Version: "1.13.0", URL: "https://example.com/coredns", Path: files["arm64"][1].ObjectKey, Digest: digest, Size: 9}},
	}, Images: map[string]dependencies.Image{"pause": {Version: "3.10.2", Source: pauseImage, Path: "mirror/registry.k8s.io/pause", Digest: digest, Size: 8}}}
	image := dependencies.Image{Version: "3.10.2", Source: pauseImage, Path: "mirror/registry.k8s.io/pause", Digest: digest}
	pending, imagePending := pendingDependencies(published, files, &image)
	if imagePending || image.Size != 8 || files["arm64"][0].Size != 10 {
		t.Fatalf("published entries were not reused: imagePending=%t image=%#v files=%#v", imagePending, image, files)
	}
	if len(pending["arm64"]) != 1 || pending["arm64"][0].Key != "coredns" {
		t.Fatalf("pending = %#v, want only coredns", pending)
	}
}

// TestDependencyUploadPlanIncludesAllFilesAndImages verifies publication starts with a stable total.
func TestDependencyUploadPlanIncludesAllFilesAndImages(t *testing.T) {
	t.Parallel()
	files := map[string][]dependencies.Artifact{
		"arm64": {{Path: filepath.Join("cache", "kubelet"), Architecture: "arm64", Size: 10}},
		"amd64": {{Path: filepath.Join("cache", "kubelet"), Architecture: "amd64", Size: 20}},
	}
	plan := dependencyUploadPlan(files, true, 30)
	if len(plan) != 3 {
		t.Fatalf("upload plan = %#v, want three transfers", plan)
	}
	var total int64
	for _, event := range plan {
		if event.Type != tui.Queued {
			t.Fatalf("upload plan event = %#v, want queued", event)
		}
		total += event.Total
	}
	if total != 60 || plan[0].Name != "kubelet (amd64)" || plan[1].Name != "kubelet (arm64)" || plan[2].Name != pauseImage {
		t.Fatalf("upload plan = %#v, want all transfers totaling 60 bytes", plan)
	}
}
