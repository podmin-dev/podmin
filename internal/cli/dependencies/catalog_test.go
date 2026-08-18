// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCatalogDefinitions verifies every dependency has complete expandable download metadata.
func TestCatalogDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, dependency := range Catalog {
		if dependency.Key == "" || seen[dependency.Key] {
			t.Fatalf("invalid or duplicate dependency key %q", dependency.Key)
		}
		seen[dependency.Key] = true
		for _, architecture := range []string{"amd64", "arm64"} {
			upstream := dependency.Architectures[architecture]
			asset := expand(dependency.AssetName, "1.2.3", upstream, "", "")
			url := expand(dependency.AssetURL, "1.2.3", upstream, asset, "")
			checksumURL := expand(dependency.ChecksumURL, "1.2.3", upstream, asset, url)
			checksumName := expand(dependency.ChecksumName, "1.2.3", upstream, asset, url)
			if upstream == "" || asset == "" || checksumName == "" || !strings.HasPrefix(url, "https://") || !strings.HasPrefix(checksumURL, "https://") || strings.Contains(asset+url+checksumURL+checksumName, "{") {
				t.Fatalf("incomplete %s/%s catalog definition", dependency.Key, architecture)
			}
		}
		if dependency.Releases == "" || dependency.ObjectName == "" || (dependency.ChecksumAlgorithm != "sha256" && dependency.ChecksumAlgorithm != "sha512") {
			t.Fatalf("incomplete %s catalog definition", dependency.Key)
		}
	}
}

// TestResolveMinorConstraint verifies resolution remains within a requested minor release.
func TestResolveMinorConstraint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`[
 {"tag_name":"v1.37.0"},
 {"tag_name":"v1.36.3"},
 {"tag_name":"v1.36.4-rc.0"},
 {"tag_name":"v1.36.2"}
]`))
	}))
	defer server.Close()
	got, err := Resolve(context.Background(), server.Client(), Dependency{Key: "kubelet", Major: 1, Minor: 36, Releases: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.36.3" {
		t.Fatalf("Resolve() = %q, want v1.36.3", got)
	}
}

// TestCatalogPinsKubernetesToolsMinor verifies node tools match Kubernetes 1.36.
func TestCatalogPinsKubernetesToolsMinor(t *testing.T) {
	missing := map[string]bool{"kubelet": true, "crictl": true}
	for _, dependency := range Catalog {
		if missing[dependency.Key] {
			if dependency.Major != 1 || dependency.Minor != 36 {
				t.Fatalf("%s constraint = %d.%d, want 1.36", dependency.Key, dependency.Major, dependency.Minor)
			}
			delete(missing, dependency.Key)
		}
	}
	for dependency := range missing {
		t.Errorf("%s is missing from the dependency catalog", dependency)
	}
}

// TestCatalogPublishesZotExecutable verifies setup does not transform Zot.
func TestCatalogPublishesZotExecutable(t *testing.T) {
	for _, dependency := range Catalog {
		if dependency.Key == "zot" {
			if dependency.ObjectName != "zot" {
				t.Fatalf("Zot object name = %q, want zot", dependency.ObjectName)
			}
			return
		}
	}
	t.Fatal("zot is missing from the dependency catalog")
}

// TestSourceRootRequiresExplicitDirectory prevents checkout detection from changing normal setup behavior.
func TestSourceRootRequiresExplicitDirectory(t *testing.T) {
	if _, err := sourceRoot(""); err == nil {
		t.Fatal("empty agent source selected the current checkout")
	}
}

// TestSourceRootFindsCommentedModule verifies source discovery parses a go.mod with its required header.
func TestSourceRootFindsCommentedModule(t *testing.T) {
	root := t.TempDir()
	module := "// Podmin <https://podmin.dev>\n// Copyright The Podmin Authors\n// SPDX-License-Identifier: Apache-2.0\n\nmodule github.com/podmin-dev/podmin\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(root, "internal", "cli")
	if err := os.MkdirAll(start, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := sourceRoot(start)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("sourceRoot() = %q, want %q", got, root)
	}
}

// TestExpiredSmallVersionCounts verifies retention handles zero through three versions.
func TestExpiredSmallVersionCounts(t *testing.T) {
	now := time.Now()
	for count := 0; count <= 3; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			objects := make([]Object, count)
			for index := range objects {
				objects[index] = Object{Key: fmt.Sprint(index), Group: "group", Version: fmt.Sprint(index), Modified: now.Add(-time.Duration(index+30) * 24 * time.Hour)}
			}
			got := Expired(objects, now)
			want := 0
			if count == 3 {
				want = 1
			}
			if len(got) != want {
				t.Fatalf("Expired() returned %d objects, want %d", len(got), want)
			}
		})
	}
}
