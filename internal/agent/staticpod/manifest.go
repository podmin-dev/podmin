// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package staticpod

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podmin-dev/podmin/internal/secrets"
	corev1 "k8s.io/api/core/v1"
)

// deploymentRevision returns a stable revision for one deployment rather than the global index commit.
func deploymentRevision(deployment manifest.IndexDeployment) string {
	digest := sha512.Sum512([]byte(string(deployment.Pod) + "\n" + string(deployment.Service)))
	return hex.EncodeToString(digest[:16])
}

// identityRevision annotates a manifest with a certificate generation fingerprint.
func identityRevision(input []byte, certificate []byte) ([]byte, string, error) {
	pod, err := manifest.ParsePod(input)
	if err != nil {
		return nil, "", fmt.Errorf("parse identity manifest: %w", err)
	}
	digest := sha512.Sum512(certificate)
	revision := hex.EncodeToString(digest[:16])
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["podmin.dev/identity-revision"] = revision
	output, err := manifest.MarshalPod(pod)
	return output, revision, err
}

// build validates, fetches declared parameters, and injects mounts into one candidate.
func (r *Reconciler) build(ctx context.Context, input []byte, revision string) (struct {
	name, namespace, service string
	manifest                 []byte
	secrets                  map[string]map[string][]byte
}, error) {
	var result struct {
		name, namespace, service string
		manifest                 []byte
		secrets                  map[string]map[string][]byte
	}
	pod, err := manifest.TransformPod(input, nil, "")
	if err != nil {
		return result, err
	}
	result.name = pod.Name
	result.namespace = pod.Namespace
	if result.namespace == "" {
		result.namespace = "default"
	}
	result.service = pod.Annotations["podmin.dev/service"]
	parameterKeys, err := annotationKeys(pod.Annotations[parameterAnnotation], "Parameter Store")
	if err != nil {
		return result, err
	}
	secretKeys, err := annotationKeys(pod.Annotations[secretAnnotation], "Secrets Manager")
	if err != nil {
		return result, err
	}
	result.secrets = map[string]map[string][]byte{}
	if r.config.NodeDNS != "" {
		pod.Spec.DNSPolicy = corev1.DNSNone
		if pod.Spec.DNSConfig == nil {
			pod.Spec.DNSConfig = &corev1.PodDNSConfig{}
		}
		pod.Spec.DNSConfig.Nameservers = []string{r.config.NodeDNS}
		pod.Spec.DNSConfig.Searches = []string{result.namespace + ".svc.cluster.local", "svc.cluster.local", "cluster.local"}
	}
	providers := []struct {
		keys                         []string
		directory, volume, mountPath string
		store                        interface {
			Get(context.Context, string) ([]byte, error)
		}
	}{
		{keys: parameterKeys, directory: string(secrets.AWSParameterStore), volume: "podmin-aws-parameter-store", mountPath: "/var/run/podmin/aws-parameter-store", store: r.params},
		{keys: secretKeys, directory: string(secrets.AWSSecretsManager), volume: "podmin-aws-secrets-manager", mountPath: "/var/run/podmin/aws-secrets-manager", store: r.secrets},
	}
	for _, provider := range providers {
		if len(provider.keys) == 0 {
			continue
		}
		if err = injectProviderMount(&pod.Spec, provider.volume, filepath.Join(r.config.SecretDir, result.name, provider.directory), provider.mountPath); err != nil {
			return result, err
		}
		result.secrets[provider.directory] = map[string][]byte{}
		for _, key := range provider.keys {
			path, nameErr := secrets.Name(r.config.Cluster, result.namespace, result.name, key)
			if nameErr != nil {
				return result, nameErr
			}
			result.secrets[provider.directory][key], err = provider.store.Get(ctx, path)
			if err != nil {
				return result, fmt.Errorf("fetch %s from %s: %w", path, provider.directory, err)
			}
		}
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations["podmin.dev/revision"] = revision
	result.manifest, err = manifest.MarshalPod(pod)
	return result, err
}

// injectProviderMount adds one reserved read-only provider volume to every container.
func injectProviderMount(spec *corev1.PodSpec, volumeName, hostPath, mountPath string) error {
	for _, volume := range spec.Volumes {
		if volume.Name == volumeName {
			return fmt.Errorf("volume %q is reserved by Podmin", volumeName)
		}
	}
	directory := corev1.HostPathDirectory
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: &directory}}})
	for _, containers := range [][]corev1.Container{spec.Containers, spec.InitContainers} {
		for index := range containers {
			container := &containers[index]
			for _, existing := range container.VolumeMounts {
				if mountPathOverlaps(existing.MountPath, mountPath) {
					return fmt.Errorf("mount path %s is reserved by Podmin", mountPath)
				}
			}
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: volumeName, MountPath: mountPath, ReadOnly: true})
		}
	}
	return nil
}

// mountPathOverlaps reports whether two cleaned absolute mount paths contain one another.
func mountPathOverlaps(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left, right = filepath.Clean(left), filepath.Clean(right)
	separator := string(filepath.Separator)
	return left == right || strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

// annotationKeys validates and sorts comma-separated safe path components.
func annotationKeys(raw, provider string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, value := range strings.Split(raw, ",") {
		key := strings.TrimSpace(value)
		if !manifest.ValidID(key) || seen[key] {
			return nil, fmt.Errorf("invalid %s key %q", provider, key)
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
