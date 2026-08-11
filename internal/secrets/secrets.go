// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/podmin-dev/podmin/internal/manifest"
)

// Provider identifies a secret storage provider.
type Provider string

const (
	// AWSParameterStore identifies AWS Systems Manager Parameter Store.
	AWSParameterStore Provider = "aws-parameter-store"
	// AWSSecretsManager identifies AWS Secrets Manager.
	AWSSecretsManager Provider = "aws-secrets-manager"
)

// ErrUnsupported identifies an operation unavailable from a provider.
var ErrUnsupported = errors.New("secret operation unsupported")

// Manager manages scoped secret values.
type Manager interface {
	Create(context.Context, string, []byte) error
	Update(context.Context, string, []byte) error
	List(context.Context, string) ([]string, error)
	Archive(context.Context, string) error
	Restore(context.Context, string) error
	Destroy(context.Context, string) error
}

// ParseProvider validates and returns a provider.
func ParseProvider(value string) (Provider, error) {
	provider := Provider(value)
	if provider != AWSParameterStore && provider != AWSSecretsManager {
		return "", fmt.Errorf("unsupported secret provider %q", value)
	}
	return provider, nil
}

// Prefix validates a scope and returns its leading-slash provider name prefix.
func Prefix(cluster, namespace, pod string) (string, error) {
	if !manifest.ValidID(cluster) || !manifest.ValidNamespace(namespace) || !manifest.ValidID(pod) {
		return "", errors.New("invalid secret cluster, namespace, or Pod name")
	}
	return "/" + cluster + "/" + namespace + "/" + pod, nil
}

// Name validates a scope and key and returns its provider name.
func Name(cluster, namespace, pod, key string) (string, error) {
	prefix, err := Prefix(cluster, namespace, pod)
	if err != nil {
		return "", err
	}
	if !manifest.ValidID(key) {
		return "", errors.New("invalid secret key")
	}
	return prefix + "/" + key, nil
}
