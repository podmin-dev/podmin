// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// fakeObjects is an in-memory authoritative object store.
type fakeObjects struct {
	etag    string
	objects map[string][]byte
	reads   []string
	onRead  func(int)
}

// Get returns a fake object and ETag identity.
func (f *fakeObjects) Get(_ context.Context, key string) ([]byte, string, error) {
	if key == "deployments/index.json" {
		f.reads = append(f.reads, key)
		if f.onRead != nil {
			f.onRead(len(f.reads))
		}
	}
	body, ok := f.objects[key]
	if !ok {
		return nil, "", cloud.ErrNotFound
	}
	return body, f.etag, nil
}

// Revision returns the fake ETag.
func (f *fakeObjects) Revision(context.Context, string) (string, error) { return f.etag, nil }

// List returns no objects because global indexes never require listing.
func (f *fakeObjects) List(context.Context, string) ([]string, error) {
	return nil, errors.New("unexpected List call")
}

// fakeParameters is an in-memory parameter store with an optional failure.
type fakeParameters struct {
	values map[string][]byte
	err    error
}

// Get returns one fake parameter.
func (f *fakeParameters) Get(_ context.Context, path string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	value, ok := f.values[path]
	if !ok {
		return nil, cloud.ErrNotFound
	}
	return value, nil
}

// fakeIdentity issues recognizable workload identity files.
type fakeIdentity struct {
	revision    uint64
	certificate string
	onIssue     func()
}

// Issue returns deterministic fake material with a future renewal time.
func (f *fakeIdentity) Issue(namespace, pod, service string, now time.Time) (workload.Material, error) {
	if f.onIssue != nil {
		f.onIssue()
	}
	certificate := f.certificate
	if certificate == "" {
		certificate = "certificate:" + namespace + "/" + pod
	}
	return workload.Material{Certificate: []byte(certificate), PrivateKey: []byte("private-key"), CABundle: []byte("ca-bundle"), NotAfter: now.Add(24 * time.Hour)}, nil
}

// Revision returns the fake durable CA revision.
func (f *fakeIdentity) Revision() uint64 { return f.revision }

// indexedObjects constructs an object store containing a canonical global index.
func indexedObjects(t *testing.T, etag string, deployments map[string]manifest.IndexDeployment, payloads map[string][]byte) *fakeObjects {
	t.Helper()
	body, err := manifest.MarshalIndex(manifest.Index(deployments))
	if err != nil {
		t.Fatal(err)
	}
	payloads["deployments/index.json"] = body
	return &fakeObjects{etag: etag, objects: payloads}
}

// objectRef constructs a verified immutable object reference.
func objectRef(key string, _ []byte) manifest.IndexObject {
	return manifest.IndexObject(key)
}

// newTestReconciler constructs a reconciler rooted in one temporary directory.
func newTestReconciler(t *testing.T, objects *fakeObjects, parameters *fakeParameters, configure func(*Config)) (*Reconciler, string) {
	t.Helper()
	root := t.TempDir()
	config := Config{Cluster: "cluster", NodeGroup: "workers", StaticDir: filepath.Join(root, "static"), SecretDir: filepath.Join(root, "secrets"), Identity: &fakeIdentity{revision: 1}}
	if configure != nil {
		configure(&config)
	}
	reconciler, err := NewReconciler(config, objects, parameters, &fakeParameters{})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, root
}

// TestReconcileMountsSecretsManagerValues verifies provider isolation, binary-safe files, and read-only mounts.
func TestReconcileMountsSecretsManagerValues(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: api\n  annotations:\n    podmin.dev/aws-parameter-store: oauth-token\n    podmin.dev/aws-secrets-manager: oauth-token\nspec:\n  initContainers: [{name: init, image: registry.podmin.internal/apps/example/init:latest}]\n  containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{"workers/api": {Pod: objectRef(podKey, pod)}}, map[string][]byte{podKey: pod})
	reconciler, root := newTestReconciler(t, objects, &fakeParameters{values: map[string][]byte{"/cluster/default/api/oauth-token": []byte("parameter")}}, nil)
	reconciler.secrets = &fakeParameters{values: map[string][]byte{"/cluster/default/api/oauth-token": {0, 1, 2}}}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	parameter, err := os.ReadFile(filepath.Join(root, "secrets", "api", "aws-parameter-store", "oauth-token"))
	if err != nil || string(parameter) != "parameter" {
		t.Fatalf("Parameter Store value was not isolated: %q, %v", parameter, err)
	}
	parameterInfo, err := os.Stat(filepath.Join(root, "secrets", "api", "aws-parameter-store", "oauth-token"))
	if err != nil {
		t.Fatal(err)
	}
	if parameterInfo.Mode().Perm() != 0o444 {
		t.Fatalf("Parameter Store value mode = %v; want 0444", parameterInfo.Mode().Perm())
	}
	parameterDirectory, err := os.Stat(filepath.Join(root, "secrets", "api", "aws-parameter-store"))
	if err != nil {
		t.Fatal(err)
	}
	if parameterDirectory.Mode().Perm() != 0o755 {
		t.Fatalf("Parameter Store directory mode = %v; want 0755", parameterDirectory.Mode().Perm())
	}
	secret, err := os.ReadFile(filepath.Join(root, "secrets", "api", "aws-secrets-manager", "oauth-token"))
	if err != nil || !bytes.Equal(secret, []byte{0, 1, 2}) {
		t.Fatalf("Secrets Manager value was not materialized: %v, %v", secret, err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "static", "api.yaml"))
	if err != nil || strings.Count(string(manifestBytes), "mountPath: /var/run/podmin/aws-parameter-store") != 2 || strings.Count(string(manifestBytes), "mountPath: /var/run/podmin/aws-secrets-manager") != 2 || strings.Count(string(manifestBytes), "readOnly: true") < 6 {
		t.Fatalf("Secrets Manager mounts are not complete and read-only: %v\n%s", err, manifestBytes)
	}
}

// TestReconcileFiltersGlobalIndexAndInjectsNodeGroupState validates filtering, secrets, and namespace-aware DNS.
func TestReconcileFiltersGlobalIndexAndInjectsNodeGroupState(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: api\n  namespace: product\n  annotations:\n    podmin.dev/aws-parameter-store: token\nspec:\n  containers:\n  - name: api\n    image: registry.podmin.internal/apps/example/api:latest\n")
	ignored := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: ignored}\nspec: {containers: [{name: ignored, image: registry.podmin.internal/apps/example/ignored:latest}]}\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{
		"workers/api":   {Pod: objectRef(podKey, pod)},
		"other/ignored": {Pod: objectRef("nodegroups/other/pods/sha512/"+manifest.Digest(ignored)+".yaml", ignored)},
	}, map[string][]byte{podKey: pod})
	parameters := &fakeParameters{values: map[string][]byte{"/cluster/product/api/token": []byte("secret")}}
	reconciler, root := newTestReconciler(t, objects, parameters, func(config *Config) { config.NodeDNS = "2001:db8::53" })
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(root, "static", "api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"product.svc.cluster.local", "svc.cluster.local", "cluster.local", "podmin.dev/revision: one"} {
		if !strings.Contains(string(result), expected) {
			t.Fatalf("missing %q in manifest:\n%s", expected, result)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "static", "ignored.yaml")); !os.IsNotExist(err) {
		t.Fatalf("deployment from another NodeGroup was published: %v", err)
	}
	secret, err := os.ReadFile(filepath.Join(root, "secrets", "api", "aws-parameter-store", "token"))
	if err != nil || string(secret) != "secret" {
		t.Fatalf("namespaced parameter was not materialized: %q, %v", secret, err)
	}
	certificate, err := os.ReadFile(filepath.Join(root, "secrets", "api", "identity", workload.CertificateFilename))
	if err != nil || string(certificate) != "certificate:product/api" {
		t.Fatalf("workload identity was not materialized: %q, %v", certificate, err)
	}
	certificateInfo, err := os.Stat(filepath.Join(root, "secrets", "api", "identity", workload.CertificateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if certificateInfo.Mode().Perm() != 0o444 {
		t.Fatalf("workload identity mode = %v; want 0444", certificateInfo.Mode().Perm())
	}
	link, err := os.Readlink(filepath.Join(root, "secrets", "api", "identity"))
	if err != nil || !strings.HasPrefix(link, "identity-generations/") || !strings.Contains(string(result), "podmin.dev/identity-revision:") {
		t.Fatalf("identity generation was not atomically selected: link=%q error=%v\n%s", link, err, result)
	}
}

// TestReconcileFencesIdentityChanges verifies mixed CA revisions cannot be published.
func TestReconcileFencesIdentityChanges(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: api}\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{"workers/api": {Pod: objectRef(podKey, pod)}}, map[string][]byte{podKey: pod})
	reconciler, root := newTestReconciler(t, objects, &fakeParameters{}, nil)
	authority := reconciler.config.Identity.(*fakeIdentity)
	authority.onIssue = func() { authority.revision++ }
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("published identities while the CA revision changed")
	}
	if _, err := os.Stat(filepath.Join(root, "static", "api.yaml")); !os.IsNotExist(err) {
		t.Fatalf("manifest was published across CA revision: %v", err)
	}
}

// TestReconcileRenewalPublishesANewGeneration verifies renewal restarts the Pod onto coherent files.
func TestReconcileRenewalPublishesANewGeneration(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: api}\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{"workers/api": {Pod: objectRef(podKey, pod)}}, map[string][]byte{podKey: pod})
	reconciler, root := newTestReconciler(t, objects, &fakeParameters{}, nil)
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldLink, err := os.Readlink(filepath.Join(root, "secrets", "api", "identity"))
	if err != nil {
		t.Fatal(err)
	}
	reconciler.identities["api"] = time.Now()
	reconciler.config.Identity.(*fakeIdentity).certificate = "renewed-certificate"
	if err = reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	newLink, err := os.Readlink(filepath.Join(root, "secrets", "api", "identity"))
	if err != nil || newLink == oldLink {
		t.Fatalf("identity generation did not change: old=%q new=%q error=%v", oldLink, newLink, err)
	}
	if _, err = os.Stat(filepath.Join(root, "secrets", "api", oldLink)); err != nil {
		t.Fatalf("previous identity generation was not retained: %v", err)
	}
}

// TestReconcilePublishesAnnotationMatchedService validates that Service and deployment names may differ.
func TestReconcilePublishesAnnotationMatchedService(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: api, namespace: product, annotations: {podmin.dev/service: frontend}}\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
	service := []byte("apiVersion: v1\nkind: Service\nmetadata: {name: frontend, namespace: product}\nspec: {selector: {app: api}, ports: [{port: 80}]}\n")
	podKey, serviceKey := "nodegroups/workers/pods/sha512/"+manifest.Digest(pod)+".yaml", "services/product/frontend/sha512/"+manifest.Digest(service)+".yaml"
	serviceRef := objectRef(serviceKey, service)
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{"workers/api": {Pod: objectRef(podKey, pod), Service: serviceRef}}, map[string][]byte{podKey: pod, serviceKey: service})
	var published []manifest.Service
	reconciler, _ := newTestReconciler(t, objects, &fakeParameters{}, func(config *Config) { config.PublishServices = func(value []manifest.Service) { published = value } })
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Name != "frontend" || published[0].Namespace != "product" {
		t.Fatalf("unexpected published Services: %#v", published)
	}
}

// TestReconcileConfirmsIndexAfterStaging verifies the final fence occurs after sync and before publication.
func TestReconcileConfirmsIndexAfterStaging(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: api}\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{
		"workers/api": {Pod: objectRef(podKey, pod)},
	}, map[string][]byte{podKey: pod})
	reconciler, root := newTestReconciler(t, objects, &fakeParameters{}, nil)
	objects.onRead = func(read int) {
		if read != 2 {
			return
		}
		temporary, err := filepath.Glob(filepath.Join(root, "static", ".podmin-*"))
		if err != nil || len(temporary) != 1 {
			t.Fatalf("index confirmed before all files were staged: paths=%v error=%v", temporary, err)
		}
		if _, err = os.Stat(filepath.Join(root, "static", "api.yaml")); !os.IsNotExist(err) {
			t.Fatalf("manifest committed before index confirmation: %v", err)
		}
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileRejectsServiceObjectKeyPayloadMismatch verifies payload identity is bound to its object key.
func TestReconcileRejectsServiceObjectKeyPayloadMismatch(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: api, annotations: {podmin.dev/service: frontend}}\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
	service := []byte("apiVersion: v1\nkind: Service\nmetadata: {name: frontend}\nspec: {selector: {app: api}, ports: [{port: 80}]}\n")
	podKey := "nodegroups/workers/pods/sha512/" + manifest.Digest(pod) + ".yaml"
	serviceKey := "services/default/wrong/sha512/" + manifest.Digest(service) + ".yaml"
	serviceRef := objectRef(serviceKey, service)
	objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{
		"workers/api": {Pod: objectRef(podKey, pod), Service: serviceRef},
	}, map[string][]byte{podKey: pod, serviceKey: service})
	reconciler, _ := newTestReconciler(t, objects, &fakeParameters{}, nil)
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("accepted Service payload that did not match object key")
	}
}

// TestReconcileRejectsServiceReferenceMismatch validates exact annotation and namespace pairing.
func TestReconcileRejectsServiceReferenceMismatch(t *testing.T) {
	tests := []struct {
		name, annotation string
		service          bool
	}{
		{name: "annotation without reference", annotation: "frontend"},
		{name: "reference without annotation", service: true},
		{name: "different annotation", annotation: "other", service: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: api\n  annotations:\n    podmin.dev/service: " + test.annotation + "\nspec: {containers: [{name: api, image: registry.podmin.internal/apps/example/api:latest}]}\n")
			service := []byte("apiVersion: v1\nkind: Service\nmetadata: {name: frontend}\nspec: {selector: {app: api}, ports: [{port: 80}]}\n")
			podKey, serviceKey := "nodegroups/workers/pods/sha512/"+manifest.Digest(pod)+".yaml", "services/default/frontend/sha512/"+manifest.Digest(service)+".yaml"
			deployment := manifest.IndexDeployment{Pod: objectRef(podKey, pod)}
			payloads := map[string][]byte{podKey: pod}
			if test.service {
				ref := objectRef(serviceKey, service)
				deployment.Service, payloads[serviceKey] = ref, service
			}
			objects := indexedObjects(t, "one", map[string]manifest.IndexDeployment{"workers/api": deployment}, payloads)
			reconciler, _ := newTestReconciler(t, objects, &fakeParameters{}, nil)
			if err := reconciler.Reconcile(context.Background()); err == nil {
				t.Fatal("expected Service pairing failure")
			}
		})
	}
}

// TestReconcileTreatsMissingGlobalIndexAsEmpty verifies a new cluster has empty desired state.
func TestReconcileTreatsMissingGlobalIndexAsEmpty(t *testing.T) {
	objects := &fakeObjects{etag: "one", objects: map[string][]byte{}}
	reconciler, _ := newTestReconciler(t, objects, &fakeParameters{}, nil)
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("missing global index error = %v", err)
	}
	if len(objects.reads) != 2 || objects.reads[0] != "deployments/index.json" || objects.reads[1] != "deployments/index.json" {
		t.Fatalf("unexpected authoritative reads: %#v", objects.reads)
	}
}

// TestCleanupRemovesUndeclaredValues prevents stale secrets remaining mounted.
func TestCleanupRemovesUndeclaredValues(t *testing.T) {
	root := t.TempDir()
	staticDir, secretDir := filepath.Join(root, "static"), filepath.Join(root, "secrets")
	if err := os.MkdirAll(staticDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "stale.yaml"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "api", "aws-parameter-store", "old")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(staticDir, secretDir, map[string]bool{"api": true}, map[string]map[string]map[string]bool{"api": {}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secret); !os.IsNotExist(err) {
		t.Fatalf("stale secret exists: %v", err)
	}
}

// TestMountPathOverlapsNormalizesReservedAliases verifies parent, child, and trailing-slash aliases.
func TestMountPathOverlapsNormalizesReservedAliases(t *testing.T) {
	reserved := "/var/run/podmin/aws-parameter-store"
	for _, path := range []string{reserved, reserved + "/", reserved + "/nested", "/var/run/podmin"} {
		if !mountPathOverlaps(path, reserved) {
			t.Errorf("path %q did not overlap reserved mount", path)
		}
	}
	if mountPathOverlaps("/var/run/example", reserved) {
		t.Fatal("unrelated mount overlapped reserved path")
	}
}
