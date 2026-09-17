// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveJoolSelectsTheNewestBuildForEveryKernel verifies ABI matching does not depend on GitHub's latest-release marker.
func TestResolveJoolSelectsTheNewestBuildForEveryKernel(t *testing.T) {
	const checksum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases":
			_, _ = fmt.Fprintf(writer, `[
{"tag_name":"jool-4.1.15-kernel-6.12.94+deb13","assets":[
  {"name":"SHA256SUMS","browser_download_url":"%[1]s/sums-94"},
  {"name":"jool-4.1.15-6.12.94+deb13-arm64.tar.gz","browser_download_url":"%[1]s/94-arm64"}
]},
{"tag_name":"jool-4.1.14-kernel-6.12.107+deb13","assets":[]},
{"tag_name":"jool-4.1.15-kernel-6.12.107+deb13","assets":[
  {"name":"SHA256SUMS","browser_download_url":"%[1]s/sums-107"},
  {"name":"jool-4.1.15-6.12.107+deb13-arm64.tar.gz","browser_download_url":"%[1]s/107-arm64"}
]},
{"tag_name":"jool-4.1.16-kernel-6.12.108+deb13","draft":true,"assets":[]}
]`, server.URL)
		case "/sums-94":
			_, _ = fmt.Fprintf(writer, "%s  jool-4.1.15-6.12.94+deb13-arm64.tar.gz\n", checksum)
		case "/sums-107":
			_, _ = fmt.Fprintf(writer, "%s  jool-4.1.15-6.12.107+deb13-arm64.tar.gz\n", checksum)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	upstream := joolReleases
	joolReleases = server.URL + "/releases"
	t.Cleanup(func() { joolReleases = upstream })

	fetcher := Fetcher{Client: server.Client(), CacheDir: t.TempDir()}
	modules, err := fetcher.ResolveJool(context.Background(), []string{"6.12.107+deb13-cloud-arm64", "6.12.94+deb13-cloud-arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 2 {
		t.Fatalf("ResolveJool() returned %d modules, want 2", len(modules))
	}
	got := map[string]JoolModule{}
	for _, module := range modules {
		got[module.Kernel] = module
	}
	module := got["6.12.107+deb13-cloud-arm64"]
	if module.Artifact.URL != server.URL+"/107-arm64" || module.Artifact.Digest != "sha256:"+checksum || !strings.Contains(module.Artifact.ObjectKey, "jool-v4.1.15-kernel-6.12.107+deb13-linux-arm64.tar.gz") {
		t.Fatalf("selected module = %#v", module)
	}
	if got["6.12.94+deb13-cloud-arm64"].Artifact.URL != server.URL+"/94-arm64" {
		t.Fatalf("selected older kernel module = %#v", got["6.12.94+deb13-cloud-arm64"])
	}
	if _, exists := got["6.12.108+deb13-cloud-arm64"]; exists {
		t.Fatal("draft Jool release was selected")
	}
}
