// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package userdata

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUserDataBashSyntax verifies rendered user-data is valid Bash for every architecture.
func TestUserDataBashSyntax(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			data, err := testUserData(architecture).Render()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				`default_runtime_name = "runsc"`,
				`staticPodPath: /etc/podmin/manifests`,
				`PodsAPI: true`,
				`/var/lib/kubelet/pods-api/pods-api.sock`,
				`net.ipv6.conf.all.forwarding = 1`,
				`net.ipv4.ip_local_reserved_ports = 30000-32767`,
				`does not meet the TCX baseline (6.6 or newer)`,
				`forward . 127.0.0.1:1053`,
				`"anonymousPolicy": ["read"]`,
				`command -v aws`,
				`ipv6-prefix`,
				`aws ec2 modify-instance-attribute`,
				`--no-source-dest-check`,
				`"type": "host-local"`,
				`"type": "ptp"`,
				`--ipv6-prefix=`,
				`install_service containerd`,
				`systemctl enable containerd zot podmin-agent coredns kubelet`,
				`systemctl start kubelet`,
				`systemctl start podmin-agent coredns`,
			} {
				if !strings.Contains(string(data), want) {
					t.Errorf("rendered user-data does not contain %q", want)
				}
			}
			for _, sentinel := range []string{"PODMIN_BUCKET", "PODMIN_CLUSTER", "PODMIN_DEPENDENCIES"} {
				if strings.Contains(string(data), sentinel) {
					t.Errorf("rendered user-data contains sentinel %q", sentinel)
				}
			}
			if strings.Contains(string(data), `"rootdirectory"`) {
				t.Error("rendered Zot config contains an S3 root directory")
			}
			for _, unwanted := range []string{`"type": "bridge"`, `"bridge": "podmin0"`, `"hairpinMode"`} {
				if strings.Contains(string(data), unwanted) {
					t.Errorf("rendered user-data contains obsolete bridge setting %q", unwanted)
				}
			}

			name := filepath.Join(t.TempDir(), "user-data.sh")
			if err := os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command("bash", "-n", name).CombinedOutput(); err != nil {
				t.Fatalf("bash -n: %v\n%s", err, output)
			}
		})
	}
}

// TestUserDataRejectsUnsafeValues verifies unsafe shell and architecture values fail validation.
func TestUserDataRejectsUnsafeValues(t *testing.T) {
	badBucket := testUserData("arm64")
	badBucket.Bucket = "bad'bucket"
	badCluster := testUserData("arm64")
	badCluster.Cluster = "Bad"
	badObject := testUserData("arm64")
	badObject.Dependencies[0].ObjectKey = "dependencies/containerd/*"
	wrongArchitecture := testUserData("arm64")
	wrongArchitecture.Dependencies[0].Architecture = "amd64"
	missingDependency := testUserData("arm64")
	missingDependency.Dependencies = missingDependency.Dependencies[1:]
	tests := []UserData{badBucket, badCluster, badObject, wrongArchitecture, missingDependency}
	for i, test := range tests {
		if _, err := test.Render(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

// TestUserDataCompressed verifies AWS user-data is compressed and excludes source comments.
func TestUserDataCompressed(t *testing.T) {
	script, err := testUserData("arm64").Render()
	if err != nil {
		t.Fatal(err)
	}
	data, err := Compress(script)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Compress(script)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("compressed user-data is not deterministic")
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(decompressed, []byte("#!/usr/bin/env bash\n")) {
		t.Fatal("compressed user-data has no shebang")
	}
	if bytes.Contains(decompressed, []byte("# Download")) || bytes.Contains(decompressed, []byte("Copyright")) {
		t.Fatal("compressed user-data contains source comments")
	}
	command := exec.Command("bash", "-n")
	command.Stdin = bytes.NewReader(decompressed)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, output)
	}
}

// TestStripCommentsPreservesShellData verifies comments are stripped only outside heredocs.
func TestStripCommentsPreservesShellData(t *testing.T) {
	script := []byte("#!/usr/bin/env bash\n# remove\nvalue=\"$(cat <<<\"x\")\"\ncat <<'EOF'\n# preserve\nEOF\n")
	want := "#!/usr/bin/env bash\nvalue=\"$(cat <<<\"x\")\"\ncat <<'EOF'\n# preserve\nEOF\n"
	if got := string(stripComments(script)); got != want {
		t.Fatalf("unexpected stripped script:\n%s", got)
	}
}

// testUserData returns complete user-data input for one architecture.
func testUserData(architecture string) UserData {
	names := []string{
		"containerd.tar.gz",
		"gvisor.tar.bz2",
		"cni-plugins.tar.gz",
		"kubelet",
		"coredns.tar.gz",
		"zot",
		"podmin-agent.tar.gz",
	}
	dependencies := make([]Dependency, 0, len(names))
	for _, name := range names {
		dependencies = append(dependencies, Dependency{
			Name:         name,
			ObjectKey:    "dependencies/" + strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".tar.bz2") + "/v1-linux-" + architecture + "-" + name,
			Digest:       "sha512:" + strings.Repeat("a", 128),
			Architecture: architecture,
		})
	}
	return UserData{
		Bucket:       "podmin-example",
		Region:       "us-west-2",
		Cluster:      "example",
		NodeGroup:    "default",
		Architecture: architecture,
		PauseImage:   "registry.podmin.internal/mirror/registry.k8s.io/pause:3.10",
		Dependencies: dependencies,
	}
}
