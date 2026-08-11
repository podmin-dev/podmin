// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// secretsReader is the read operation used by Secrets.
type secretsReader interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// secretsManager is the AWS Secrets Manager management subset used by the CLI.
type secretsManager interface {
	CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	ListSecrets(context.Context, *secretsmanager.ListSecretsInput, ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
	DeleteSecret(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	RestoreSecret(context.Context, *secretsmanager.RestoreSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error)
}

// Secrets returns explicitly named current Secrets Manager values.
type Secrets struct {
	reader  secretsReader
	manager secretsManager
}

// Get fetches the current AWSCURRENT SecretString or SecretBinary value.
func (s *Secrets) Get(ctx context.Context, name string) ([]byte, error) {
	out, err := s.reader.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
	if err != nil {
		return nil, err
	}
	if out.SecretString != nil {
		return []byte(*out.SecretString), nil
	}
	if out.SecretBinary != nil {
		return append([]byte(nil), out.SecretBinary...), nil
	}
	return nil, errors.New("secret has no value")
}

// secretValue assigns the correct AWS representation after validating size.
func secretValue(value []byte) (*string, []byte, error) {
	if len(value) > 65536 {
		return nil, nil, errors.New("Secrets Manager value exceeds 65536 bytes")
	}
	if utf8.Valid(value) {
		return aws.String(string(value)), nil, nil
	}
	return nil, append([]byte(nil), value...), nil
}

// Create creates a new secret.
func (s *Secrets) Create(ctx context.Context, name string, value []byte) error {
	text, binary, err := secretValue(value)
	if err != nil {
		return err
	}
	_, err = s.manager.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String(name), SecretString: text, SecretBinary: binary})
	var exists *types.ResourceExistsException
	if errors.As(err, &exists) {
		return cloud.ErrExists
	}
	return err
}

// Update adds a current value to an existing secret.
func (s *Secrets) Update(ctx context.Context, name string, value []byte) error {
	text, binary, err := secretValue(value)
	if err != nil {
		return err
	}
	_, err = s.manager.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(name), SecretString: text, SecretBinary: binary})
	return err
}

// List returns sorted immediate valid keys under the exact scope prefix.
func (s *Secrets) List(ctx context.Context, prefix string) ([]string, error) {
	seen := map[string]bool{}
	var token *string
	for {
		out, err := s.manager.ListSecrets(ctx, &secretsmanager.ListSecretsInput{NextToken: token, Filters: []types.Filter{{Key: types.FilterNameStringTypeName, Values: []string{prefix + "/"}}}})
		if err != nil {
			return nil, err
		}
		for _, entry := range out.SecretList {
			if entry.DeletedDate != nil {
				continue
			}
			name := aws.ToString(entry.Name)
			key, ok := strings.CutPrefix(name, prefix+"/")
			if ok && !strings.Contains(key, "/") && manifest.ValidID(key) {
				seen[key] = true
			}
		}
		token = out.NextToken
		if token == nil {
			break
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Archive schedules deletion with a 30-day recovery window.
func (s *Secrets) Archive(ctx context.Context, name string) error {
	days := int64(30)
	_, err := s.manager.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(name), RecoveryWindowInDays: &days})
	return ignoreMissingSecret(err)
}

// Restore cancels a secret's scheduled deletion.
func (s *Secrets) Restore(ctx context.Context, name string) error {
	_, err := s.manager.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{SecretId: aws.String(name)})
	return err
}

// Destroy permanently deletes a secret without recovery.
func (s *Secrets) Destroy(ctx context.Context, name string) error {
	_, err := s.manager.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(name), ForceDeleteWithoutRecovery: aws.Bool(true)})
	return ignoreMissingSecret(err)
}

// ignoreMissingSecret makes deletion idempotent.
func ignoreMissingSecret(err error) error {
	var missing *types.ResourceNotFoundException
	if errors.As(err, &missing) {
		return nil
	}
	return err
}
