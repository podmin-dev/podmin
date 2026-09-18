// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import "testing"

// TestParseProvider accepts only the two supported AWS providers.
func TestParseProvider(t *testing.T) {
	for _, value := range []string{string(AWSParameterStore), string(AWSSecretsManager)} {
		if provider, err := ParseProvider(value); err != nil || string(provider) != value {
			t.Fatalf("ParseProvider(%q) = %q, %v", value, provider, err)
		}
	}
	if _, err := ParseProvider("unknown"); err == nil {
		t.Fatal("unknown provider was accepted")
	}
}

// TestName validates and constructs namespaced Pod secret names.
func TestName(t *testing.T) {
	name, err := Name("cluster", "product", "api", "token")
	if err != nil || name != "/cluster/product/api/token" {
		t.Fatalf("Name() = %q, %v", name, err)
	}
	if _, err = Name("cluster", "_system", "api", "token"); err == nil {
		t.Fatal("invalid Kubernetes namespace was accepted")
	}
}

// TestSystemName permits only explicitly user-manageable system secrets.
func TestSystemName(t *testing.T) {
	name, err := SystemName("cluster", OTelLogsHeadersKey)
	if err != nil || name != "/cluster/_system/otel-logs-headers" {
		t.Fatalf("SystemName() = %q, %v", name, err)
	}
	for _, key := range []string{"cluster-ca", "workload-ca-key", "other"} {
		if _, err = SystemName("cluster", key); err == nil {
			t.Errorf("SystemName() accepted internal key %q", key)
		}
	}
}
