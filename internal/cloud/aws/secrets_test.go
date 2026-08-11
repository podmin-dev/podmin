// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/podmin-dev/podmin/internal/cloud"
)

// fakeSecretsManager returns one injected output and records its input.
type fakeSecretsManager struct {
	input  *secretsmanager.GetSecretValueInput
	output *secretsmanager.GetSecretValueOutput
}

// fakeSecretsOperations records Secrets Manager mutation and listing inputs.
type fakeSecretsOperations struct {
	created   *secretsmanager.CreateSecretInput
	updated   *secretsmanager.PutSecretValueInput
	deleted   *secretsmanager.DeleteSecretInput
	restored  *secretsmanager.RestoreSecretInput
	listed    *secretsmanager.ListSecretsOutput
	createErr error
}

// CreateSecret records secret creation.
func (f *fakeSecretsOperations) CreateSecret(_ context.Context, input *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	f.created = input
	return new(secretsmanager.CreateSecretOutput), f.createErr
}

// PutSecretValue records a secret update.
func (f *fakeSecretsOperations) PutSecretValue(_ context.Context, input *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.updated = input
	return new(secretsmanager.PutSecretValueOutput), nil
}

// ListSecrets returns the configured secret listing.
func (f *fakeSecretsOperations) ListSecrets(context.Context, *secretsmanager.ListSecretsInput, ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	return f.listed, nil
}

// DeleteSecret records recoverable or permanent deletion.
func (f *fakeSecretsOperations) DeleteSecret(_ context.Context, input *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	f.deleted = input
	return new(secretsmanager.DeleteSecretOutput), nil
}

// RestoreSecret records restoration.
func (f *fakeSecretsOperations) RestoreSecret(_ context.Context, input *secretsmanager.RestoreSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error) {
	f.restored = input
	return new(secretsmanager.RestoreSecretOutput), nil
}

// GetSecretValue returns the configured fake output.
func (f *fakeSecretsManager) GetSecretValue(_ context.Context, input *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.input = input
	return f.output, nil
}

// TestSecretsReadsStringAndBinary verifies exact names and both AWS value representations.
func TestSecretsReadsStringAndBinary(t *testing.T) {
	for _, output := range []*secretsmanager.GetSecretValueOutput{{SecretString: aws.String("value")}, {SecretBinary: []byte{0, 1, 2}}} {
		client := &fakeSecretsManager{output: output}
		store := &Secrets{reader: client}
		value, err := store.Get(context.Background(), "/cluster/product/api/token")
		if err != nil || aws.ToString(client.input.SecretId) != "/cluster/product/api/token" || len(value) == 0 {
			t.Fatalf("unexpected secret result: %v, %v, %#v", value, err, client.input)
		}
	}
}

// TestSecretsRejectsMissingValue verifies malformed provider responses fail closed.
func TestSecretsRejectsMissingValue(t *testing.T) {
	store := &Secrets{reader: &fakeSecretsManager{output: new(secretsmanager.GetSecretValueOutput)}}
	if _, err := store.Get(context.Background(), "missing"); err == nil {
		t.Fatal("accepted a secret without a value")
	}
}

// TestSecretsManagement verifies binary writes, scoped listings, recovery, and destruction.
func TestSecretsManagement(t *testing.T) {
	client := &fakeSecretsOperations{listed: &secretsmanager.ListSecretsOutput{SecretList: []types.SecretListEntry{{Name: aws.String("/cluster/default/api/b")}, {Name: aws.String("/cluster/default/api/a")}, {Name: aws.String("/cluster/default/api/nested/key")}}}}
	store := &Secrets{manager: client}
	if err := store.Create(context.Background(), "/cluster/default/api/token", []byte{0xff}); err != nil || len(client.created.SecretBinary) != 1 || client.created.SecretString != nil {
		t.Fatalf("create binary secret: %v, %#v", err, client.created)
	}
	names, err := store.List(context.Background(), "/cluster/default/api")
	if err != nil || len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("list secrets: %v, %#v", err, names)
	}
	if err = store.Archive(context.Background(), "/cluster/default/api/token"); err != nil || aws.ToInt64(client.deleted.RecoveryWindowInDays) != 30 {
		t.Fatalf("archive secret: %v, %#v", err, client.deleted)
	}
	if err = store.Restore(context.Background(), "/cluster/default/api/token"); err != nil || aws.ToString(client.restored.SecretId) == "" {
		t.Fatalf("restore secret: %v, %#v", err, client.restored)
	}
	if err = store.Destroy(context.Background(), "/cluster/default/api/token"); err != nil || !aws.ToBool(client.deleted.ForceDeleteWithoutRecovery) {
		t.Fatalf("destroy secret: %v, %#v", err, client.deleted)
	}
	client.createErr = new(types.ResourceExistsException)
	if err = store.Create(context.Background(), "existing", []byte("value")); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("create existing secret = %v", err)
	}
}
