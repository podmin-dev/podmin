// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// Listing describes one deployment in committed cluster desired state.
type Listing struct {
	Name      string
	Namespace string
	NodeGroup string
	Service   string
	Origin    string
}

// listingStore is the object API needed to read committed desired state.
type listingStore interface {
	Get(context.Context, string) ([]byte, string, error)
}

// List reads and validates every deployment in committed cluster desired state.
func List(ctx context.Context, store listingStore) ([]Listing, error) {
	body, _, err := store.Get(ctx, "deployments/index.json")
	if errors.Is(err, cloud.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	index, err := manifest.ParseIndex(body)
	if err != nil {
		return nil, err
	}
	listings := make([]Listing, 0, len(index))
	for key, deployment := range index {
		nodeGroup, name, _ := manifest.DeploymentIdentity(key)
		podBody, _, getErr := store.Get(ctx, string(deployment.Pod))
		if getErr != nil {
			return nil, fmt.Errorf("read deployment %q Pod: %w", key, getErr)
		}
		if verifyErr := deployment.Pod.Verify(podBody); verifyErr != nil {
			return nil, fmt.Errorf("verify deployment %q Pod: %w", key, verifyErr)
		}
		pod, parseErr := manifest.ParsePod(podBody)
		if parseErr != nil {
			return nil, fmt.Errorf("parse deployment %q Pod: %w", key, parseErr)
		}
		if pod.Name != name || !manifest.ValidNamespace(pod.Namespace) {
			return nil, fmt.Errorf("deployment %q Pod identity does not match index", key)
		}
		service := "-"
		if deployment.Service != "" {
			_, service, _, _ = manifest.ServiceIdentity(string(deployment.Service))
		}
		origin := "workload"
		if name == "cloudflared" && pod.Namespace == "platform-cloudflared" && pod.Annotations[manifest.InstallAnnotation] == "cloudflared" {
			origin = "install/cloudflared"
		}
		listings = append(listings, Listing{Name: name, Namespace: pod.Namespace, NodeGroup: nodeGroup, Service: service, Origin: origin})
	}
	sort.Slice(listings, func(i, j int) bool {
		if listings[i].NodeGroup != listings[j].NodeGroup {
			return listings[i].NodeGroup < listings[j].NodeGroup
		}
		return listings[i].Name < listings[j].Name
	})
	return listings, nil
}
