// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"reflect"
	"testing"

	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// memoryIndexStore records one in-memory index object.
type memoryIndexStore struct {
	key     string
	body    []byte
	objects map[string][]byte
}

// Get returns the stored index or a missing-object error.
func (s *memoryIndexStore) Get(_ context.Context, key string) ([]byte, string, error) {
	if body, ok := s.objects[key]; ok {
		return body, "etag", nil
	}
	if s.body == nil {
		return nil, "", cloud.ErrNotFound
	}
	return s.body, "etag", nil
}

// PutIfMatch records a conditional index write.
func (s *memoryIndexStore) PutIfMatch(_ context.Context, key string, body []byte, _ string) error {
	if key != "deployments/index.json" {
		if s.objects == nil {
			s.objects = map[string][]byte{}
		}
		if _, exists := s.objects[key]; exists {
			return cloud.ErrPrecondition
		}
		s.objects[key] = append([]byte(nil), body...)
		return nil
	}
	s.key, s.body = key, append([]byte(nil), body...)
	return nil
}

// TestMutateIndexStartsGlobalIndexEmpty verifies the hard-migration storage root.
func TestMutateIndexStartsGlobalIndexEmpty(t *testing.T) {
	store := &memoryIndexStore{}
	err := mutateIndex(context.Background(), store, func(index *manifest.Index) error {
		if len(*index) != 0 {
			t.Fatal("missing index was not initialized empty")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.key != "deployments/index.json" {
		t.Fatalf("index written to %q", store.key)
	}
}

// TestApplyPublishesPayloadsBeforeCommittingIndex verifies the deployment boundary.
func TestApplyPublishesPayloadsBeforeCommittingIndex(t *testing.T) {
	store := &memoryIndexStore{}
	input := manifest.Deployment{Pod: []byte("pod"), ServiceYAML: []byte("service"), Service: &manifest.Service{Name: "api", Namespace: "product"}}
	if err := Apply(context.Background(), store, "workers", "web", input); err != nil {
		t.Fatal(err)
	}
	index, err := manifest.ParseIndex(store.body)
	if err != nil {
		t.Fatal(err)
	}
	entry := index["workers/web"]
	if string(store.objects[string(entry.Pod)]) != "pod" || string(store.objects[string(entry.Service)]) != "service" {
		t.Fatalf("published objects = %#v, index = %#v", store.objects, index)
	}
}

// TestListReportsSortedDesiredStateAndInstallOrigin verifies authoritative inventory projection.
func TestListReportsSortedDesiredStateAndInstallOrigin(t *testing.T) {
	store := &memoryIndexStore{}
	cloudflared := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: cloudflared\n  namespace: platform-cloudflared\n  annotations:\n    podmin.dev/install: cloudflared\nspec:\n  containers:\n    - name: cloudflared\n      image: registry.podmin.internal/mirror/index.docker.io/cloudflare/cloudflared:2026.8.2\n")
	hello := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: hello\n  namespace: default\nspec:\n  containers:\n    - name: hello\n      image: registry.podmin.internal/apps/hello:latest\n")
	if err := Apply(context.Background(), store, "ingress", "cloudflared", manifest.Deployment{Pod: cloudflared}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), store, "default", "hello", manifest.Deployment{Pod: hello, ServiceYAML: []byte("service"), Service: &manifest.Service{Name: "hello", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	listings, err := List(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	want := []Listing{
		{Name: "hello", Namespace: "default", NodeGroup: "default", Service: "hello", Origin: "workload"},
		{Name: "cloudflared", Namespace: "platform-cloudflared", NodeGroup: "ingress", Service: "-", Origin: "install/cloudflared"},
	}
	if !reflect.DeepEqual(listings, want) {
		t.Fatalf("List() = %#v, want %#v", listings, want)
	}
}

// TestListTreatsAMissingIndexAsEmpty verifies a new cluster has an empty inventory.
func TestListTreatsAMissingIndexAsEmpty(t *testing.T) {
	listings, err := List(context.Background(), &memoryIndexStore{})
	if err != nil || len(listings) != 0 {
		t.Fatalf("List() = %#v, %v", listings, err)
	}
}
