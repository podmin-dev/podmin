// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
)

// installSecretStore is an in-memory secret listing for install tests.
type installSecretStore struct{ keys []string }

// Create is unused by cloudflared installation.
func (s *installSecretStore) Create(context.Context, string, []byte) error { return nil }

// Update is unused by cloudflared installation.
func (s *installSecretStore) Update(context.Context, string, []byte) error { return nil }

// List returns the configured secret keys.
func (s *installSecretStore) List(context.Context, string) ([]string, error) { return s.keys, nil }

// Archive is unused by cloudflared installation.
func (s *installSecretStore) Archive(context.Context, string) error { return nil }

// Restore is unused by cloudflared installation.
func (s *installSecretStore) Restore(context.Context, string) error { return nil }

// Destroy is unused by cloudflared installation.
func (s *installSecretStore) Destroy(context.Context, string) error { return nil }

// installObjectStore is an in-memory object store for install tests.
type installObjectStore struct{ objects map[string][]byte }

// Get returns one stored object and a synthetic version.
func (s *installObjectStore) Get(_ context.Context, key string) ([]byte, string, error) {
	body, ok := s.objects[key]
	if !ok {
		return nil, "", cloud.ErrNotFound
	}
	return body, "etag", nil
}

// PutStream stores streamed bytes.
func (s *installObjectStore) PutStream(_ context.Context, key string, body io.Reader, _ int64) error {
	value, err := io.ReadAll(body)
	if err == nil {
		s.objects[key] = value
	}
	return err
}

// PutIfMatch stores conditionally published bytes.
func (s *installObjectStore) PutIfMatch(_ context.Context, key string, body []byte, _ string) error {
	s.objects[key] = append([]byte(nil), body...)
	return nil
}

// List returns no retained objects.
func (s *installObjectStore) List(context.Context, string) ([]cloud.ObjectInfo, error) {
	return nil, nil
}

// Delete removes one object.
func (s *installObjectStore) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	return nil
}

// TestCloudflaredUsesAnExistingMirrorAndCommitsDesiredState verifies the idempotent happy path.
func TestCloudflaredUsesAnExistingMirrorAndCommitsDesiredState(t *testing.T) {
	index := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"` + cloudflaredDigest + `","size":1,"annotations":{"org.opencontainers.image.ref.name":"2026.8.2"}}]}`)
	objects := &installObjectStore{objects: map[string][]byte{"mirror/index.docker.io/cloudflare/cloudflared/index.json": index}}
	store := &installSecretStore{keys: []string{cloudflaredSecret}}
	client := &cloud.Client{Objects: objects, SecretStores: map[secrets.Provider]secrets.Manager{secrets.AWSParameterStore: store}}
	options := Options{Context: config.Context{ClusterID: "example", Provider: "aws", SecretsProvider: string(secrets.AWSParameterStore)}, NodeGroup: "default"}
	if err := Cloudflared(context.Background(), client, options, nil); err != nil {
		t.Fatal(err)
	}
	deployments, err := manifest.ParseIndex(objects.objects["deployments/index.json"])
	if err != nil {
		t.Fatal(err)
	}
	pod := string(objects.objects[string(deployments["default/cloudflared"].Pod)])
	for _, expected := range []string{"namespace: platform-cloudflared", "podmin.dev/install: cloudflared", "podmin.dev/aws-parameter-store: tunnel-token", "registry.podmin.internal/mirror/index.docker.io/cloudflare/cloudflared:2026.8.2", "/var/run/podmin/aws-parameter-store/tunnel-token", "path: /ready", "readOnlyRootFilesystem: true"} {
		if !strings.Contains(pod, expected) {
			t.Errorf("cloudflared Pod lacks %q:\n%s", expected, pod)
		}
	}
}

// TestCloudflaredRequiresThePredefinedToken verifies install does not mutate secrets.
func TestCloudflaredRequiresThePredefinedToken(t *testing.T) {
	objects := &installObjectStore{objects: map[string][]byte{}}
	store := &installSecretStore{}
	client := &cloud.Client{Objects: objects, SecretStores: map[secrets.Provider]secrets.Manager{secrets.AWSSecretsManager: store}}
	options := Options{Context: config.Context{ClusterID: "example", Provider: "aws", SecretsProvider: string(secrets.AWSSecretsManager)}, NodeGroup: "default"}
	err := Cloudflared(context.Background(), client, options, nil)
	if err == nil || !strings.Contains(err.Error(), "/example/platform-cloudflared/cloudflared/tunnel-token") {
		t.Fatalf("Cloudflared() error = %v", err)
	}
}
