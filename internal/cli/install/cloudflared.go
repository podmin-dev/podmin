// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"text/template"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/deploy"
	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
)

const (
	cloudflaredName      = "cloudflared"
	cloudflaredNamespace = "platform-cloudflared"
	cloudflaredSecret    = "tunnel-token"
	cloudflaredSource    = "docker.io/cloudflare/cloudflared:2026.8.2"
	cloudflaredDigest    = "sha256:0aa26e284f05e6c77ae375b8c9c11d9eb6a448fb7bcd8d40f31cb6176189eb38"
)

//go:embed cloudflared.yaml
var cloudflaredManifest string

// Options contains the selected cloudflared installation inputs.
type Options struct {
	Context   config.Context
	NodeGroup string
	Provider  string
}

// Cloudflared verifies its token, mirrors its pinned image, and commits its static Pod.
func Cloudflared(ctx context.Context, client *cloud.Client, options Options, progress tui.Progress) error {
	if !manifest.ValidID(options.NodeGroup) {
		return errors.New("invalid nodegroup ID")
	}
	providerName := options.Provider
	if providerName == "" {
		providerName = options.Context.SecretsProvider
	}
	provider, err := secrets.ParseProvider(providerName)
	if err != nil {
		return err
	}
	store, ok := client.SecretStores[provider]
	if !ok {
		return fmt.Errorf("secret provider %q is unavailable from cloud provider %q", provider, options.Context.Provider)
	}
	if progress != nil {
		progress(tui.Event{Type: tui.Status, Message: "Verifying Cloudflare Tunnel token..."})
	}
	prefix, err := secrets.Prefix(options.Context.ClusterID, cloudflaredNamespace, cloudflaredName)
	if err != nil {
		return err
	}
	keys, err := store.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("list cloudflared secrets: %w", err)
	}
	if !slices.Contains(keys, cloudflaredSecret) {
		return fmt.Errorf("cloudflared requires secret %q at %s/%s; create it with `podmin secret create %s --for %s --namespace %s`", cloudflaredSecret, prefix, cloudflaredSecret, cloudflaredSecret, cloudflaredName, cloudflaredNamespace)
	}
	image, mirrored, err := images.Mirrored(ctx, cloudflaredSource, cloudflaredDigest, client.Objects)
	if err != nil {
		return fmt.Errorf("inspect cloudflared mirror: %w", err)
	}
	if !mirrored {
		if progress != nil {
			progress(tui.Event{Type: tui.Status, Message: "Resolving pinned cloudflared image..."})
		}
		resolved, resolveErr := images.ResolveDigest(ctx, cloudflaredSource)
		if resolveErr != nil {
			return resolveErr
		}
		if resolved != cloudflaredDigest {
			return fmt.Errorf("cloudflared image digest is %s, expected %s", resolved, cloudflaredDigest)
		}
		cache, cacheErr := images.CacheRoot()
		if cacheErr != nil {
			return cacheErr
		}
		if err = images.PullIfMissing(ctx, cloudflaredSource, cloudflaredDigest, cache, progress); err != nil {
			return fmt.Errorf("download cloudflared image: %w", err)
		}
		image, err = images.Mirror(ctx, cloudflaredSource, cache, client.Objects, progress)
		if err != nil {
			return fmt.Errorf("publish cloudflared image: %w", err)
		}
	}
	deployment, err := cloudflaredDeployment(options.NodeGroup, provider, image)
	if err != nil {
		return err
	}
	if progress != nil {
		progress(tui.Event{Type: tui.Status, Message: "Committing cloudflared deployment..."})
	}
	return deploy.Apply(ctx, client.Objects, options.NodeGroup, cloudflaredName, deployment)
}

// cloudflaredDeployment constructs and validates Podmin's built-in cloudflared workload.
func cloudflaredDeployment(nodeGroup string, provider secrets.Provider, image string) (manifest.Deployment, error) {
	annotation, mountPath := "", ""
	switch provider {
	case secrets.AWSParameterStore:
		annotation = "podmin.dev/aws-parameter-store"
		mountPath = "/var/run/podmin/aws-parameter-store/" + cloudflaredSecret
	case secrets.AWSSecretsManager:
		annotation = "podmin.dev/aws-secrets-manager"
		mountPath = "/var/run/podmin/aws-secrets-manager/" + cloudflaredSecret
	default:
		return manifest.Deployment{}, fmt.Errorf("unsupported cloudflared secret provider %q", provider)
	}
	tmpl, err := template.New("cloudflared.yaml").Option("missingkey=error").Parse(cloudflaredManifest)
	if err != nil {
		return manifest.Deployment{}, fmt.Errorf("parse embedded cloudflared manifest: %w", err)
	}
	data := struct {
		InstallAnnotation string
		SecretAnnotation  string
		NodeGroup         string
		Image             string
		TokenPath         string
		TrustBundlePath   string
	}{
		InstallAnnotation: manifest.InstallAnnotation,
		SecretAnnotation:  annotation,
		NodeGroup:         nodeGroup,
		Image:             image,
		TokenPath:         mountPath,
		TrustBundlePath:   manifest.IdentityMountPath + "/ca.crt",
	}
	var body bytes.Buffer
	if err = tmpl.Execute(&body, data); err != nil {
		return manifest.Deployment{}, fmt.Errorf("render embedded cloudflared manifest: %w", err)
	}
	return manifest.ParseDeployment(body.Bytes(), nil, "", cloudflaredName, nodeGroup)
}
