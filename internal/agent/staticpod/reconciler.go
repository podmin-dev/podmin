// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

const (
	parameterAnnotation = "podmin.dev/aws-parameter-store"
	secretAnnotation    = "podmin.dev/aws-secrets-manager"
)

// Config contains runtime configuration; StaticDir must be exclusively Podmin-owned because reconciliation removes stale YAML files.
type Config struct {
	Cluster, NodeGroup, StaticDir, SecretDir, NodeDNS string
	PollInterval, ReconcileTimeout                    time.Duration
	PublishServices                                   func([]manifest.Service)
	Identity                                          IdentityAuthority
}

// IdentityAuthority issues workload certificates from synchronized cluster CA state.
type IdentityAuthority interface {
	Issue(string, string, string, time.Time) (workload.Material, error)
	Revision() uint64
}

// ObjectStore supplies authoritative revision and Pod objects.
type ObjectStore interface {
	Get(context.Context, string) ([]byte, string, error)
}

// SecretReader fetches one explicitly named configuration or secret value.
type SecretReader interface {
	Get(context.Context, string) ([]byte, error)
}

// Reconciler serializes complete candidate construction and publication.
type Reconciler struct {
	config           Config
	objects          ObjectStore
	params           SecretReader
	secrets          SecretReader
	mu               sync.Mutex
	servicesMu       sync.RWMutex
	indexETag        string
	health           atomic.Bool
	services         []manifest.Service
	identities       map[string]time.Time
	identityRevision uint64
}

// NewReconciler creates a reconciler using injectable directories and providers.
func NewReconciler(config Config, objects ObjectStore, params SecretReader, secrets SecretReader) (*Reconciler, error) {
	if config.Cluster == "" || config.NodeGroup == "" || config.StaticDir == "" || config.SecretDir == "" || objects == nil || params == nil || secrets == nil || config.Identity == nil {
		return nil, errors.New("cluster, node group, directories, object store, parameter store, secret store, and identity authority are required")
	}
	return &Reconciler{config: config, objects: objects, params: params, secrets: secrets, identities: map[string]time.Time{}}, nil
}

// Healthy reports whether the most recent reconciliation succeeded.
func (r *Reconciler) Healthy() bool { return r.health.Load() }

// Services returns a defensive snapshot of the most recently published Service state.
func (r *Reconciler) Services() []manifest.Service {
	r.servicesMu.RLock()
	defer r.servicesMu.RUnlock()
	return cloneServices(r.services)
}

// Reconcile stages a complete changed index before sequential per-file atomic commits.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	authorityRevision := r.config.Identity.Revision()
	const indexKey = "deployments/index.json"
	indexBody, indexETag, indexErr := r.objects.Get(ctx, indexKey)
	index := manifest.Index{}
	if errors.Is(indexErr, cloud.ErrNotFound) {
		indexErr = nil
	} else if indexErr == nil {
		index, indexErr = manifest.ParseIndex(indexBody)
	}
	if indexErr != nil {
		r.health.Store(false)
		return fmt.Errorf("read index: %w", indexErr)
	}
	if indexETag == r.indexETag && r.health.Load() && r.config.Identity.Revision() == r.identityRevision {
		renew := false
		for _, expiry := range r.identities {
			renew = renew || workload.NeedsRenewal(expiry, time.Now())
		}
		if !renew {
			return nil
		}
	}
	type selectedDeployment struct {
		name       string
		deployment manifest.IndexDeployment
	}
	var selected []selectedDeployment
	for key, deployment := range index {
		nodeGroup, name, _ := manifest.DeploymentIdentity(key)
		if nodeGroup == r.config.NodeGroup {
			selected = append(selected, selectedDeployment{name: name, deployment: deployment})
		}
	}
	type candidate struct {
		name, service      string
		manifest           []byte
		secrets            map[string]map[string][]byte
		identity           workload.Material
		identityGeneration string
	}
	candidates := make([]candidate, 0, len(selected))
	services := make([]manifest.Service, 0, len(selected))
	names := map[string]bool{}
	serviceNames := map[string]bool{}
	for _, item := range selected {
		deployment := item.deployment
		podKey := string(deployment.Pod)
		input, _, getErr := r.objects.Get(ctx, podKey)
		if getErr != nil {
			r.health.Store(false)
			return fmt.Errorf("get %s: %w", podKey, getErr)
		}
		if verifyErr := deployment.Pod.Verify(input); verifyErr != nil {
			r.health.Store(false)
			return fmt.Errorf("verify %s: %w", podKey, verifyErr)
		}
		c, buildErr := r.build(ctx, input, indexETag)
		if buildErr != nil {
			r.health.Store(false)
			return fmt.Errorf("build %s: %w", podKey, buildErr)
		}
		if c.name != item.name {
			r.health.Store(false)
			return fmt.Errorf("indexed deployment %q contains Pod %q", item.name, c.name)
		}
		if names[c.name] {
			r.health.Store(false)
			return fmt.Errorf("duplicate Pod name %q", c.name)
		}
		names[c.name] = true
		material, issueErr := r.config.Identity.Issue(c.namespace, c.name, c.service, time.Now())
		if issueErr != nil {
			r.health.Store(false)
			return fmt.Errorf("issue identity for Pod %q: %w", c.name, issueErr)
		}
		identityManifest, identityGeneration, annotationErr := identityRevision(c.manifest, material.Certificate)
		if annotationErr != nil {
			r.health.Store(false)
			return fmt.Errorf("annotate identity for Pod %q: %w", c.name, annotationErr)
		}
		candidates = append(candidates, candidate{name: c.name, service: c.service, manifest: identityManifest, secrets: c.secrets, identity: material, identityGeneration: identityGeneration})
		if deployment.Service == "" {
			if c.service != "" {
				r.health.Store(false)
				return fmt.Errorf("pod %q annotates unreferenced Service %q", c.name, c.service)
			}
			continue
		}
		serviceKey := string(deployment.Service)
		serviceInput, _, getErr := r.objects.Get(ctx, serviceKey)
		if getErr != nil {
			r.health.Store(false)
			return fmt.Errorf("get %s: %w", serviceKey, getErr)
		}
		if verifyErr := deployment.Service.Verify(serviceInput); verifyErr != nil {
			r.health.Store(false)
			return fmt.Errorf("verify %s: %w", serviceKey, verifyErr)
		}
		service, parseErr := manifest.ParseService(serviceInput)
		if parseErr != nil {
			r.health.Store(false)
			return fmt.Errorf("validate %s: %w", serviceKey, parseErr)
		}
		if c.service != service.Name || c.namespace != service.Namespace {
			r.health.Store(false)
			return fmt.Errorf("referenced Service %q does not exactly match Pod %q annotation and namespace", service.Name, c.name)
		}
		keyNamespace, keyName, _, keyOK := manifest.ServiceIdentity(serviceKey)
		if !keyOK || keyNamespace != service.Namespace || keyName != service.Name {
			r.health.Store(false)
			return fmt.Errorf("referenced Service payload %q/%q does not match object key identity", service.Namespace, service.Name)
		}
		identity := service.Namespace + "/" + service.Name
		if serviceNames[identity] {
			r.health.Store(false)
			return fmt.Errorf("duplicate Service %q", identity)
		}
		serviceNames[identity] = true
		services = append(services, service)
	}
	var staged []stagedChange
	defer func() {
		for _, file := range staged {
			file.discard()
		}
	}()
	for _, c := range candidates {
		identityFiles := map[string][]byte{workload.CertificateFilename: c.identity.Certificate, workload.PrivateKeyFilename: c.identity.PrivateKey, workload.CABundleFilename: c.identity.CABundle}
		for name, value := range identityFiles {
			file, stageErr := stageFile(filepath.Join(r.config.SecretDir, c.name, "identity-generations", c.identityGeneration, name), value, 0o400)
			if stageErr != nil {
				r.health.Store(false)
				return stageErr
			}
			staged = append(staged, file)
		}
		link, stageErr := stageSymlink(filepath.Join(r.config.SecretDir, c.name, "identity"), filepath.Join("identity-generations", c.identityGeneration))
		if stageErr != nil {
			r.health.Store(false)
			return stageErr
		}
		staged = append(staged, link)
		for provider, values := range c.secrets {
			for key, value := range values {
				file, stageErr := stageFile(filepath.Join(r.config.SecretDir, c.name, provider, key), value, 0o400)
				if stageErr != nil {
					r.health.Store(false)
					return stageErr
				}
				staged = append(staged, file)
			}
		}
		file, stageErr := stageFile(filepath.Join(r.config.StaticDir, c.name+".yaml"), c.manifest, 0o600)
		if stageErr != nil {
			r.health.Store(false)
			return stageErr
		}
		staged = append(staged, file)
	}
	_, confirmedETag, err := r.objects.Get(ctx, indexKey)
	if errors.Is(err, cloud.ErrNotFound) && indexETag == "" {
		err = nil
	}
	if err != nil {
		r.health.Store(false)
		return fmt.Errorf("confirm index: %w", err)
	}
	if confirmedETag != indexETag {
		r.health.Store(false)
		return errors.New("index changed while constructing desired state")
	}
	if r.config.Identity.Revision() != authorityRevision {
		r.health.Store(false)
		return errors.New("workload CA changed while constructing desired state")
	}
	for _, file := range staged {
		if err = file.commit(); err != nil {
			r.health.Store(false)
			return err
		}
	}
	keep := map[string]bool{}
	desiredSecrets := map[string]map[string]map[string]bool{}
	for _, c := range candidates {
		keep[c.name] = true
		desiredSecrets[c.name] = map[string]map[string]bool{}
		for provider, values := range c.secrets {
			desiredSecrets[c.name][provider] = map[string]bool{}
			for key := range values {
				desiredSecrets[c.name][provider][key] = true
			}
		}
	}
	if err = cleanup(r.config.StaticDir, r.config.SecretDir, keep, desiredSecrets); err != nil {
		r.health.Store(false)
		return err
	}
	desiredIdentities := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		desiredIdentities[candidate.name] = candidate.identityGeneration
	}
	if err = cleanupIdentityGenerations(r.config.SecretDir, desiredIdentities); err != nil {
		r.health.Store(false)
		return err
	}
	r.servicesMu.Lock()
	r.services = cloneServices(services)
	r.servicesMu.Unlock()
	r.identities = make(map[string]time.Time, len(candidates))
	for _, candidate := range candidates {
		r.identities[candidate.name] = candidate.identity.NotAfter
	}
	r.identityRevision = authorityRevision
	if r.config.PublishServices != nil {
		r.config.PublishServices(cloneServices(services))
	}
	r.indexETag = indexETag
	r.health.Store(true)
	return nil
}

// cloneServices creates a snapshot callers cannot mutate through maps or slices.
func cloneServices(input []manifest.Service) []manifest.Service {
	result := make([]manifest.Service, len(input))
	for i, service := range input {
		result[i] = service
		result[i].Selector = make(map[string]string, len(service.Selector))
		for key, value := range service.Selector {
			result[i].Selector[key] = value
		}
		result[i].Ports = append([]manifest.ServicePort(nil), service.Ports...)
	}
	return result
}
