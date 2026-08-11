// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/podmin-dev/podmin/internal/agent/identity"
	"github.com/podmin-dev/podmin/internal/buildvars"
	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/podmin-dev/podmin/internal/cli/userdata"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
)

const pauseImage = "registry.k8s.io/pause:3.10.2"

// Options contains user-selected setup inputs and command streams.
type Options struct {
	Context     config.Context
	VPCCIDR     string
	NodeGroups  []string
	AgentSource string
	AutoApprove bool
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

// Run creates or updates a cluster from the authoritative setup options.
func Run(ctx context.Context, client *cloud.Client, options Options) error {
	prefix, err := netip.ParsePrefix(options.VPCCIDR)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
		return errors.New("--vpc-cidr must be a private IPv4 CIDR")
	}
	nodeGroups, err := parseNodeGroups(options.NodeGroups)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(nodeGroups))
	for name := range nodeGroups {
		names = append(names, name)
	}
	subnetCIDRs, err := client.Compute.SubnetCIDRs(ctx, options.Context.ClusterID, prefix.Masked(), names)
	if err != nil {
		return err
	}
	if err = resolveArchitectures(ctx, client.Compute, nodeGroups); err != nil {
		return err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	artifacts, err := publishDependencies(ctx, client.Objects, nodeGroups, filepath.Join(cache, "dependencies"), options.AgentSource)
	if err != nil {
		return err
	}
	if err = addUserData(options.Context, nodeGroups, artifacts); err != nil {
		return err
	}
	imageCache, err := images.CacheRoot()
	if err != nil {
		return err
	}
	if _, err = images.Mirror(ctx, pauseImage, imageCache, client.Objects); err != nil {
		return fmt.Errorf("mirror pause image: %w", err)
	}
	if err = ensureCertificateAuthorities(ctx, client.SystemSecrets, options.Context.ClusterID); err != nil {
		return err
	}
	variables := infra.Variables{ClusterID: options.Context.ClusterID, Region: options.Context.Region, Profile: options.Context.Profile, Bucket: options.Context.Bucket, VPCCIDR: prefix.Masked().String(), SubnetCIDRs: subnetCIDRs, NodeGroups: nodeGroups}
	if err = infra.Run(ctx, variables, false, options.AutoApprove, options.Stdin, options.Stdout, options.Stderr); err != nil {
		return err
	}
	infrastructure, err := json.MarshalIndent(variables, "", "  ")
	if err != nil {
		return err
	}
	if err = client.Objects.Put(ctx, "tfstate/podmin.auto.tfvars.json", infrastructure); err != nil {
		return err
	}
	if err = cleanupDependencies(ctx, client.Objects); err != nil {
		_, _ = fmt.Fprintf(options.Stderr, "warning: dependency cleanup failed: %v\n", err)
	}
	return nil
}

// parseNodeGroups validates the authoritative NodeGroup definitions.
func parseNodeGroups(values []string) (map[string]infra.NodeGroup, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one --nodegroup is required")
	}
	if len(values) > 256 {
		return nil, errors.New("at most 256 NodeGroups fit in an Amazon-provided IPv6 /56")
	}
	nodeGroups := make(map[string]infra.NodeGroup, len(values))
	for _, value := range values {
		name, nodeGroup, err := infra.ParseNodeGroup(value)
		if err != nil {
			return nil, err
		}
		if !manifest.ValidID(name) {
			return nil, fmt.Errorf("invalid NodeGroup ID %q", name)
		}
		if _, exists := nodeGroups[name]; exists {
			return nil, fmt.Errorf("duplicate NodeGroup %q", name)
		}
		nodeGroups[name] = nodeGroup
	}
	return nodeGroups, nil
}

// resolveArchitectures records the architecture of each NodeGroup's instance type.
func resolveArchitectures(ctx context.Context, compute cloud.Compute, nodeGroups map[string]infra.NodeGroup) error {
	for name, nodeGroup := range nodeGroups {
		architecture, err := compute.Architecture(ctx, nodeGroup.InstanceType)
		if err != nil {
			return err
		}
		nodeGroup.Architecture = architecture
		nodeGroups[name] = nodeGroup
	}
	return nil
}

// publishDependencies fetches and uploads one artifact set per architecture.
func publishDependencies(ctx context.Context, objects cloud.ObjectStore, nodeGroups map[string]infra.NodeGroup, cacheDir, agentSource string) (map[string][]dependencies.Artifact, error) {
	result := map[string][]dependencies.Artifact{}
	fetcher := dependencies.Fetcher{CacheDir: cacheDir, SourceDir: agentSource, AgentVersion: buildvars.BuildVersion()}
	for _, nodeGroup := range nodeGroups {
		if _, exists := result[nodeGroup.Architecture]; exists {
			continue
		}
		artifacts, err := fetcher.Fetch(ctx, nodeGroup.Architecture)
		if err != nil {
			return nil, err
		}
		for _, artifact := range artifacts {
			body, err := os.ReadFile(artifact.Path)
			if err != nil {
				return nil, err
			}
			if err = objects.Put(ctx, artifact.ObjectKey, body); err != nil {
				return nil, err
			}
		}
		result[nodeGroup.Architecture] = artifacts
	}
	return result, nil
}

// addUserData renders and attaches each NodeGroup's bootstrap script.
func addUserData(selected config.Context, nodeGroups map[string]infra.NodeGroup, artifacts map[string][]dependencies.Artifact) error {
	for name, nodeGroup := range nodeGroups {
		inputs := make([]userdata.Dependency, 0, len(artifacts[nodeGroup.Architecture]))
		for _, artifact := range artifacts[nodeGroup.Architecture] {
			inputs = append(inputs, userdata.Dependency{Name: artifact.Name, ObjectKey: artifact.ObjectKey, Digest: artifact.Digest, Architecture: artifact.Architecture})
		}
		userData := userdata.UserData{Bucket: selected.Bucket, Region: selected.Region, Cluster: selected.ClusterID, NodeGroup: name, Architecture: nodeGroup.Architecture, PauseImage: "registry.podmin.internal/mirror/" + pauseImage, Dependencies: inputs}
		readable, err := userData.Render()
		if err != nil {
			return err
		}
		compressed, err := userdata.Compress(readable)
		if err != nil {
			return err
		}
		nodeGroup.UserData = base64.StdEncoding.EncodeToString(compressed)
		nodeGroups[name] = nodeGroup
	}
	return nil
}

// ensureCertificateAuthorities creates the workload and cluster CAs once.
func ensureCertificateAuthorities(ctx context.Context, store secrets.Manager, cluster string) error {
	key := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate workload CA key: %w", err)
	}
	if err := store.Create(ctx, "/"+cluster+"/_system/workload-ca-key", []byte(base64.StdEncoding.EncodeToString(key))); err != nil && !errors.Is(err, cloud.ErrExists) {
		return fmt.Errorf("create workload CA key: %w", err)
	}
	clusterCA, err := identity.Generate(cluster)
	if err != nil {
		return fmt.Errorf("generate cluster CA: %w", err)
	}
	if err = store.Create(ctx, "/"+cluster+identity.ClusterCAPathSuffix, clusterCA); err != nil && !errors.Is(err, cloud.ErrExists) {
		return fmt.Errorf("create cluster CA (workload CA may already exist): %w", err)
	}
	return nil
}
