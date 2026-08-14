// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// indexStore is the conditional object API needed to commit desired state.
type indexStore interface {
	Get(context.Context, string) ([]byte, string, error)
	PutIfMatch(context.Context, string, []byte, string) error
}

// Apply publishes immutable payloads and commits one deployment to desired state.
func Apply(ctx context.Context, store indexStore, nodeGroup, name string, deployment manifest.Deployment) error {
	if !manifest.ValidID(nodeGroup) || !manifest.ValidID(name) {
		return errors.New("invalid deployment name or nodegroup ID")
	}
	podKey := fmt.Sprintf("nodegroups/%s/pods/sha512/%s.yaml", nodeGroup, manifest.Digest(deployment.Pod))
	if err := store.PutIfMatch(ctx, podKey, deployment.Pod, ""); err != nil && !errors.Is(err, cloud.ErrPrecondition) {
		return err
	}
	var service manifest.IndexObject
	if deployment.Service != nil {
		serviceKey := fmt.Sprintf("services/%s/%s/sha512/%s.yaml", deployment.Service.Namespace, deployment.Service.Name, manifest.Digest(deployment.ServiceYAML))
		if err := store.PutIfMatch(ctx, serviceKey, deployment.ServiceYAML, ""); err != nil && !errors.Is(err, cloud.ErrPrecondition) {
			return err
		}
		service = manifest.IndexObject(serviceKey)
	}
	return mutateIndex(ctx, store, func(index *manifest.Index) error {
		(*index)[manifest.DeploymentKey(nodeGroup, name)] = manifest.IndexDeployment{Pod: manifest.IndexObject(podKey), Service: service}
		return nil
	})
}

// Delete removes one deployment from desired state.
func Delete(ctx context.Context, store indexStore, nodeGroup, name string) error {
	if !manifest.ValidID(nodeGroup) || !manifest.ValidID(name) {
		return errors.New("invalid deployment name or nodegroup ID")
	}
	return mutateIndex(ctx, store, func(index *manifest.Index) error {
		delete(*index, manifest.DeploymentKey(nodeGroup, name))
		return nil
	})
}

// mutateIndex rereads and reapplies mutation after every competing CAS commit.
func mutateIndex(ctx context.Context, store indexStore, mutation func(*manifest.Index) error) error {
	key := "deployments/index.json"
	for attempts := 0; attempts < 16; attempts++ {
		body, etag, err := store.Get(ctx, key)
		var index manifest.Index
		if errors.Is(err, cloud.ErrNotFound) {
			index = manifest.Index{}
			err = nil
		} else if err == nil {
			index, err = manifest.ParseIndex(body)
		}
		if err != nil {
			return err
		}
		if err = mutation(&index); err != nil {
			return err
		}
		body, err = manifest.MarshalIndex(index)
		if err != nil {
			return err
		}
		if err = store.PutIfMatch(ctx, key, body, etag); errors.Is(err, cloud.ErrPrecondition) {
			continue
		}
		return err
	}
	return errors.New("index changed too frequently")
}
