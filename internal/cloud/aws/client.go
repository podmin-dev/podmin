// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"

	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/secrets"
)

// New loads AWS configuration using an optional shared-config profile.
func New(ctx context.Context, region, profile, bucket string) (*cloud.Client, error) {
	config, err := Load(ctx, region, profile)
	if err != nil {
		return nil, err
	}
	objects := config.ObjectStore(bucket, 0)
	parameterStore := config.ParameterStore()
	secretStore := config.Secrets()
	return &cloud.Client{
		Bucket:        objects,
		Objects:       objects,
		SecretStores:  map[secrets.Provider]secrets.Manager{secrets.AWSParameterStore: parameterStore, secrets.AWSSecretsManager: secretStore},
		SystemSecrets: parameterStore,
		Compute:       config.Compute(),
	}, nil
}
