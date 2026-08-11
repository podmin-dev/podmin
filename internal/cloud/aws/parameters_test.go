// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/podmin-dev/podmin/internal/cloud"
)

// fakeParameterReader returns one injected output and records its input.
type fakeParameterReader struct {
	input  *ssm.GetParameterInput
	output *ssm.GetParameterOutput
}

// GetParameter returns the configured fake output.
func (f *fakeParameterReader) GetParameter(_ context.Context, input *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.input = input
	return f.output, nil
}

// fakeParameterStore records management operations and returns configured results.
type fakeParameterStore struct {
	put, deleted *string
	listed       *ssm.GetParametersByPathOutput
	putErr       error
	deleteErr    error
}

// PutParameter records a write.
func (f *fakeParameterStore) PutParameter(_ context.Context, input *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	f.put = input.Name
	return new(ssm.PutParameterOutput), f.putErr
}

// GetParametersByPath returns the configured listing.
func (f *fakeParameterStore) GetParametersByPath(context.Context, *ssm.GetParametersByPathInput, ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	return f.listed, nil
}

// DeleteParameter records a deletion.
func (f *fakeParameterStore) DeleteParameter(_ context.Context, input *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	f.deleted = input.Name
	return new(ssm.DeleteParameterOutput), f.deleteErr
}

// TestParameterStoreReadsDecryptedParameter verifies the exact path and mandatory decryption.
func TestParameterStoreReadsDecryptedParameter(t *testing.T) {
	client := &fakeParameterReader{output: &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("value")}}}
	store := &ParameterStore{reader: client}
	value, err := store.Get(context.Background(), "/cluster/product/api/token")
	if err != nil || string(value) != "value" || aws.ToString(client.input.Name) != "/cluster/product/api/token" || !aws.ToBool(client.input.WithDecryption) {
		t.Fatalf("unexpected parameter result: %q, %v, %#v", value, err, client.input)
	}
}

// TestParameterStoreRejectsMissingValue verifies malformed provider responses fail closed.
func TestParameterStoreRejectsMissingValue(t *testing.T) {
	store := &ParameterStore{reader: &fakeParameterReader{output: new(ssm.GetParameterOutput)}}
	if _, err := store.Get(context.Background(), "missing"); err == nil {
		t.Fatal("accepted a parameter without a value")
	}
}

// TestParameterStoreManagement verifies create conflicts, sorted listings, and idempotent deletion.
func TestParameterStoreManagement(t *testing.T) {
	client := &fakeParameterStore{listed: &ssm.GetParametersByPathOutput{Parameters: []types.Parameter{{Name: aws.String("/root/b")}, {Name: aws.String("/root/a")}}}}
	store := &ParameterStore{store: client}
	if err := store.Create(context.Background(), "/root/key", []byte("value")); err != nil || aws.ToString(client.put) != "/root/key" {
		t.Fatalf("put parameter: %v, %q", err, aws.ToString(client.put))
	}
	names, err := store.List(context.Background(), "/root")
	if err != nil || len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("list parameters: %v, %#v", err, names)
	}
	client.putErr = new(types.ParameterAlreadyExists)
	if err = store.Create(context.Background(), "/root/key", []byte("value")); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("create conflict = %v", err)
	}
	client.deleteErr = new(types.ParameterNotFound)
	if err = store.Destroy(context.Background(), "/root/key"); err != nil || aws.ToString(client.deleted) != "/root/key" {
		t.Fatalf("delete parameter: %v, %q", err, aws.ToString(client.deleted))
	}
}
