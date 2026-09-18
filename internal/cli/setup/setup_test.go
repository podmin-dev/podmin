// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podmin-dev/podmin/internal/cli/userdata"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/secrets"
)

// listingSecretStore supplies configured keys while embedding unused management operations.
type listingSecretStore struct {
	secrets.Manager
	keys []string
}

// List returns the configured secret keys.
func (s *listingSecretStore) List(context.Context, string) ([]string, error) { return s.keys, nil }

// TestParseNodeGroups validates defaults, duplicates, and malformed names.
func TestParseNodeGroups(t *testing.T) {
	nodeGroups, err := parseNodeGroups([]string{"workers", "api,size=2,instance-type=m7g.large,zone=b,nat64=t4g.small"})
	if err != nil {
		t.Fatal(err)
	}
	if nodeGroups["workers"].Size != 1 || nodeGroups["api"].Size != 2 || nodeGroups["api"].InstanceType != "m7g.large" || nodeGroups["api"].Zone != "b" || nodeGroups["api"].NAT64InstanceType != "t4g.small" {
		t.Fatalf("NodeGroups = %#v", nodeGroups)
	}
	for _, values := range [][]string{nil, {"workers", "workers"}, {"Not Valid"}} {
		if _, err = parseNodeGroups(values); err == nil {
			t.Fatalf("parseNodeGroups(%q) succeeded", values)
		}
	}
}

// TestParseNAT64 validates opt-in shared and dedicated NAT64 instance configuration.
func TestParseNAT64(t *testing.T) {
	nodeGroups := map[string]infra.NodeGroup{"default": {}}
	if got, err := parseNAT64("", nodeGroups); err != nil || got != nil {
		t.Fatalf("parseNAT64 disabled = %#v, %v", got, err)
	}
	got, err := parseNAT64("instance-type=t4g.nano", nodeGroups)
	if err != nil || got.InstanceType != "t4g.nano" || got.Generation == "" {
		t.Fatalf("parseNAT64 enabled = %#v, %v", got, err)
	}
	nodeGroups["default"] = infra.NodeGroup{NAT64InstanceType: "t4g.small"}
	if _, err = parseNAT64("", nodeGroups); err == nil {
		t.Fatal("parseNAT64 accepted a dedicated NAT64 instance without --nat64")
	}
	if _, err = parseNAT64("t4g.nano", nodeGroups); err == nil {
		t.Fatal("parseNAT64 accepted malformed shared configuration")
	}
}

// TestParseOTelLogs validates exact secure endpoints, defaults, and secret scoping.
func TestParseOTelLogs(t *testing.T) {
	selected := config.Context{ClusterID: "example", SecretsProvider: string(secrets.AWSSecretsManager)}
	if got, err := parseOTelLogs("", selected); err != nil || got != nil {
		t.Fatalf("parseOTelLogs disabled = %#v, %v", got, err)
	}
	got, err := parseOTelLogs("endpoint=https://api.openobserve.ai/api/example/v1/logs,headers-secret=true", selected)
	if err != nil || got.Host != "api.openobserve.ai" || got.Port != "443" || got.URI != "/api/example/v1/logs" || got.Protocol != "http/protobuf" || got.HeadersSecret != "/example/_system/otel-logs-headers" || got.HeadersProvider != string(secrets.AWSSecretsManager) {
		t.Fatalf("parseOTelLogs HTTP = %#v, %v", got, err)
	}
	got, err = parseOTelLogs("endpoint=https://collector.example:4317,grpc=true", selected)
	if err != nil || got.Port != "4317" || got.Protocol != "grpc" || got.URI != "/v1/logs" {
		t.Fatalf("parseOTelLogs gRPC = %#v, %v", got, err)
	}
	got, err = parseOTelLogs("endpoint=https://collector.example/v1/logs,grpc=false", selected)
	if err != nil || got.Protocol != "http/protobuf" || got.URI != "/v1/logs" {
		t.Fatalf("parseOTelLogs explicit HTTP = %#v, %v", got, err)
	}
	invalid := []string{
		"endpoint=http://collector.example/v1/logs",
		"endpoint=https://collector.example",
		"endpoint=https://collector.example/v1/logs,grpc=on",
		"endpoint=https://collector.example/path,grpc=true",
		"endpoint=https://collector.example/v1/logs,protocol=grpc",
		"endpoint=https://collector.example/v1/logs,headers-secret=false",
		"endpoint=https://collector.example/v1/logs,headers-secret=otel-logs-headers",
		"endpoint=https://collector.example/v1/logs,endpoint=https://other.example/v1/logs",
	}
	for _, value := range invalid {
		if _, err = parseOTelLogs(value, selected); err == nil {
			t.Errorf("parseOTelLogs(%q) succeeded", value)
		}
	}
}

// TestParseWorkloadCAPublication validates external PEM destinations and reserved state protection.
func TestParseWorkloadCAPublication(t *testing.T) {
	if got, err := parseWorkloadCAPublication("", "cluster-bucket"); err != nil || got != nil {
		t.Fatalf("disabled publication = %#v, %v", got, err)
	}
	got, err := parseWorkloadCAPublication("s3://trust-bucket/podmin/production/workload-ca.pem", "cluster-bucket")
	if err != nil || got.Bucket != "trust-bucket" || got.Key != "podmin/production/workload-ca.pem" {
		t.Fatalf("publication = %#v, %v", got, err)
	}
	for _, value := range []string{"https://trust-bucket/ca.pem", "s3://Bad_Bucket/ca.pem", "s3://trust-bucket", "s3://trust-bucket/../ca.pem", "s3://cluster-bucket/identity/ca.json"} {
		if _, err = parseWorkloadCAPublication(value, "cluster-bucket"); err == nil {
			t.Errorf("parseWorkloadCAPublication(%q) succeeded", value)
		}
	}
}

// TestVerifyOTelLogsSecret requires the referenced key in the selected provider.
func TestVerifyOTelLogsSecret(t *testing.T) {
	store := &listingSecretStore{keys: []string{secrets.OTelLogsHeadersKey}}
	client := &cloud.Client{SecretStores: map[secrets.Provider]secrets.Manager{secrets.AWSSecretsManager: store}}
	selected := config.Context{ClusterID: "example", Provider: "aws", SecretsProvider: string(secrets.AWSSecretsManager)}
	logs := &userdata.OTelLogs{HeadersSecret: "/example/_system/otel-logs-headers", HeadersProvider: string(secrets.AWSSecretsManager)}
	if err := verifyOTelLogsSecret(context.Background(), client, selected, logs); err != nil {
		t.Fatal(err)
	}
	store.keys = nil
	if err := verifyOTelLogsSecret(context.Background(), client, selected, logs); err == nil {
		t.Fatal("verifyOTelLogsSecret accepted a missing secret")
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
