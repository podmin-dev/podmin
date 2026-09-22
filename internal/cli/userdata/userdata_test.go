// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package userdata

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
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
				`server = "http://127.0.0.1:5000"`,
				`command -v aws`,
				`command -v python3`,
				`AWS_USE_DUALSTACK_ENDPOINT=true`,
				`Ensuring AWS SSM Agent is installed and running`,
				`[userdata] %s\tts=%s`,
				`Podmin AWS user-data has started`,
				`Podmin AWS user-data failed at line`,
				`Runtime dependency download completed successfully`,
				`Runtime dependency download failed; retrying in 3 seconds`,
				`Global node IPv6 address discovered successfully`,
				`Assigning delegated Pod IPv6 prefix to ${pod_eni}`,
				`Delegated Pod IPv6 prefix discovered successfully`,
				`Node and Pod network discovery completed successfully`,
				`Podmin user-data completed successfully`,
				`s3.dualstack.${region}.amazonaws.com`,
				`amazon-ssm-${region}/latest/debian_${architecture}`,
				`"UseDualStackEndpoint": true`,
				`python3 -m tarfile -e`,
				`ipv6-prefix`,
				`ipv6-prefix-count 1`,
				`/ipv6s`,
				`mapfile -t macs`,
				`ip -6 rule add priority 81`,
				`podmin-network.service`,
				`"type": "host-local"`,
				`"type": "ptp"`,
				`--node-address=`,
				`--ipv6-prefix=`,
				`/usr/local/bin/crictl`,
				`runtime-endpoint: unix:///run/containerd/containerd.sock`,
				`install_service containerd`,
				`services=(containerd podmin-network podmin-agent coredns kubelet)`,
				`systemctl start podmin-agent`,
				`systemctl start kubelet coredns`,
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
			if strings.Index(string(data), "Ensuring AWS SSM Agent") > strings.Index(string(data), "dependencies=(") {
				t.Error("rendered user-data installs AWS SSM Agent after dependency setup")
			}
			if strings.Index(string(data), "Podmin user-data completed successfully") < strings.Index(string(data), "systemctl start kubelet coredns") {
				t.Error("rendered user-data reports completion before starting services")
			}
			for _, unwanted := range []string{"zot", "/etc/zot.json", "kubelet.service' 'network-online.target'"} {
				if strings.Contains(string(data), unwanted) {
					t.Errorf("rendered user-data contains removed registry runtime %q", unwanted)
				}
			}
			if strings.Contains(string(data), "while read -r mac") {
				t.Error("rendered user-data drops an unterminated final IMDS MAC entry")
			}
			if strings.Contains(string(data), "modify-network-interface-attribute") || strings.Contains(string(data), "source-dest-check") {
				t.Error("rendered user-data disables AWS source/destination checks")
			}
			for _, unwanted := range []string{`command -v snap`, `dpkg -s amazon-ssm-agent`} {
				if strings.Contains(string(data), unwanted) {
					t.Errorf("rendered user-data contains unsupported SSM installation branch %q", unwanted)
				}
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
	badOTel := testUserData("arm64")
	badOTel.OTelLogs = &OTelLogs{Host: "bad host", Port: "443", URI: "/v1/logs", Protocol: "http/protobuf"}
	badProvider := testUserData("arm64")
	badProvider.OTelLogs = &OTelLogs{Host: "collector.example", Port: "443", URI: "/v1/logs", Protocol: "http/protobuf", HeadersSecret: "/example/_system/otel-logs-headers", HeadersProvider: "other"}
	badCA := testUserData("arm64")
	badCA.OTelLogs = &OTelLogs{Host: "collector.example", Port: "443", URI: "/v1/logs", Protocol: "http/protobuf", CA: "s3://trust-bucket"}
	badPublication := testUserData("arm64")
	badPublication.WorkloadCAPublishBucket = "trust-bucket"
	tests := []UserData{badBucket, badCluster, badObject, wrongArchitecture, missingDependency, badOTel, badProvider, badCA, badPublication}
	for i, test := range tests {
		if _, err := test.Render(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

// TestUserDataRendersOTelLogs verifies Fluent Bit uses the exact endpoint and runtime secret provider.
func TestUserDataRendersOTelLogs(t *testing.T) {
	for provider, command := range map[string]string{"aws-parameter-store": "aws ssm get-parameter", "aws-secrets-manager": "aws secretsmanager get-secret-value"} {
		t.Run(provider, func(t *testing.T) {
			input := testUserData("arm64")
			input.OTelLogs = &OTelLogs{Host: "api.openobserve.ai", Port: "443", URI: "/api/example/v1/logs", Protocol: "http/protobuf", MTLS: true, CA: "s3://observability/tls/logs-server-ca.pem", HeadersSecret: "/example/_system/otel-logs-headers", HeadersProvider: provider}
			input.WorkloadCAPublishBucket = "trust-bucket"
			input.WorkloadCAPublishKey = "podmin/example/workload-ca.pem"
			data, err := input.Render()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"otel_logs_enabled='true'",
				`"host": "api.openobserve.ai"`,
				`"logs_uri": "/api/example/v1/logs"`,
				command,
				`"multiline.parser": "cri"`,
				`"path": "/var/log/containers/*.log"`,
				`"logs_body_key": "$log"`,
				`"storage.total_limit_size": "1G"`,
				`output["tls.crt_file"] = "/run/podmin/fluent-bit/identity/tls.crt"`,
				`output["tls.key_file"] = "/run/podmin/fluent-bit/identity/tls.key"`,
				`output["tls.ca_file"] = "/run/podmin/fluent-bit/server-ca.pem"`,
				`--otel-logs-ca=s3://observability/tls/logs-server-ca.pem`,
				`--workload-ca-publish-bucket=${workload_ca_publish_bucket}`,
				`--workload-ca-publish-key=${workload_ca_publish_key}`,
				`--otel-logs-mtls=${otel_logs_mtls}`,
				"install_service fluent-bit",
				`dpkg --install "${destination}/libpq5.deb"`,
				`dpkg --install "${destination}/fluent-bit.deb"`,
				"systemctl start fluent-bit",
			} {
				if !strings.Contains(string(data), want) {
					t.Errorf("rendered telemetry user-data does not contain %q", want)
				}
			}
			if strings.Contains(string(data), "Authorization") {
				t.Fatal("rendered user-data contains an authentication header")
			}
			if strings.Contains(string(data), "podmin-fluent-bit-ca-refresh") || strings.Contains(string(data), "openssl") {
				t.Fatal("rendered user-data manages Fluent Bit CA material outside podmin-agent")
			}
			libpqInstall := strings.Index(string(data), `dpkg --install "${destination}/libpq5.deb"`)
			fluentBitInstall := strings.Index(string(data), `dpkg --install "${destination}/fluent-bit.deb"`)
			if libpqInstall < 0 || fluentBitInstall < 0 || libpqInstall > fluentBitInstall {
				t.Fatal("rendered user-data does not install libpq5 before Fluent Bit")
			}
			if strings.Contains(string(data), "apt-get install") {
				t.Fatal("rendered user-data can download package dependencies")
			}
		})
	}
}

// TestFluentBitConfigGeneratorProducesJSON verifies the installed helper emits a valid secret-free configuration.
func TestFluentBitConfigGeneratorProducesJSON(t *testing.T) {
	input := testUserData("arm64")
	input.OTelLogs = &OTelLogs{Host: "collector.example", Port: "4317", URI: "/v1/logs", Protocol: "grpc"}
	data, err := input.Render()
	if err != nil {
		t.Fatal(err)
	}
	_, remainder, ok := strings.Cut(string(data), "cat > /usr/local/sbin/podmin-fluent-bit-config <<'EOF'\n")
	if !ok {
		t.Fatal("rendered user-data has no Fluent Bit config generator")
	}
	script, _, ok := strings.Cut(remainder, "\nEOF\n  chmod 0700 /usr/local/sbin/podmin-fluent-bit-config")
	if !ok {
		t.Fatal("rendered user-data has no complete Fluent Bit config generator")
	}
	directory := t.TempDir()
	configuration := filepath.Join(directory, "fluent-bit.yaml")
	script = strings.ReplaceAll(script, "/etc/fluent-bit/fluent-bit.yaml", configuration)
	command := exec.Command("bash")
	command.Stdin = strings.NewReader(script)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("config generator: %v\n%s", runErr, output)
	}
	generated, err := os.ReadFile(configuration)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err = json.Unmarshal(generated, &parsed); err != nil {
		t.Fatalf("generated Fluent Bit configuration is not JSON: %v\n%s", err, generated)
	}
	for _, want := range []string{`"host": "collector.example"`, `"port": 4317`, `"grpc": "on"`, `"logs_uri": "/v1/logs"`, `"header": []`} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("generated Fluent Bit configuration does not contain %q", want)
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
		"crictl.tar.gz",
		"coredns.tar.gz",
		"libpq5.deb",
		"fluent-bit.deb",
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
