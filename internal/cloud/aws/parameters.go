// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/podmin-dev/podmin/internal/cloud"
	"github.com/podmin-dev/podmin/internal/secrets"
)

// parameterReader is the read operation used by ParameterStore.
type parameterReader interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// parameterStoreAPI is the management subset used by ParameterStore.
type parameterStoreAPI interface {
	PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	GetParametersByPath(context.Context, *ssm.GetParametersByPathInput, ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error)
	DeleteParameter(context.Context, *ssm.DeleteParameterInput, ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

// ParameterStore reads and writes AWS Systems Manager parameters.
type ParameterStore struct {
	reader parameterReader
	store  parameterStoreAPI
}

// Get returns one decrypted String, StringList, or SecureString value.
func (s *ParameterStore) Get(ctx context.Context, path string) ([]byte, error) {
	out, err := s.reader.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(path), WithDecryption: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return nil, errors.New("parameter has no value")
	}
	return []byte(*out.Parameter.Value), nil
}

// put creates or updates a SecureString parameter.
func (s *ParameterStore) put(ctx context.Context, path string, value []byte, overwrite bool) error {
	if !utf8.Valid(value) {
		return errors.New("value must be valid UTF-8 for AWS Parameter Store")
	}
	if len(value) > 4096 {
		return errors.New("value exceeds the AWS Parameter Store 4096-byte limit")
	}
	_, err := s.store.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(path), Value: aws.String(string(value)), Type: types.ParameterTypeSecureString, Overwrite: aws.Bool(overwrite)})
	var exists *types.ParameterAlreadyExists
	if errors.As(err, &exists) {
		return cloud.ErrExists
	}
	return err
}

// Create creates a SecureString parameter without overwriting one that exists.
func (s *ParameterStore) Create(ctx context.Context, name string, value []byte) error {
	return s.put(ctx, name, value, false)
}

// Update updates a SecureString parameter.
func (s *ParameterStore) Update(ctx context.Context, name string, value []byte) error {
	return s.put(ctx, name, value, true)
}

// List returns immediate parameter names beneath path without values.
func (s *ParameterStore) List(ctx context.Context, path string) ([]string, error) {
	var names []string
	var token *string
	for {
		out, err := s.store.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{Path: aws.String(path), Recursive: aws.Bool(false), NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, p := range out.Parameters {
			names = append(names, strings.TrimPrefix(aws.ToString(p.Name), path+"/"))
		}
		token = out.NextToken
		if token == nil {
			break
		}
	}
	sort.Strings(names)
	return names, nil
}

// Archive reports that Parameter Store does not support recoverable deletion.
func (s *ParameterStore) Archive(context.Context, string) error {
	return fmt.Errorf("archive Parameter Store secret: %w", secrets.ErrUnsupported)
}

// Restore reports that Parameter Store does not support restoration.
func (s *ParameterStore) Restore(context.Context, string) error {
	return fmt.Errorf("restore Parameter Store secret: %w", secrets.ErrUnsupported)
}

// Destroy permanently deletes a Parameter Store parameter.
func (s *ParameterStore) Destroy(ctx context.Context, path string) error {
	_, err := s.store.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(path)})
	var missing *types.ParameterNotFound
	if errors.As(err, &missing) {
		return nil
	}
	return err
}
