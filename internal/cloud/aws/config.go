// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Config constructs AWS resources from one SDK configuration.
type Config struct {
	region string
	sdk    aws.Config
}

// Load loads AWS configuration using an optional shared-config profile.
func Load(ctx context.Context, region, profile string) (Config, error) {
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithUseDualStackEndpoint(aws.DualStackEndpointStateEnabled),
	}
	if profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(profile))
	}
	sdk, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return Config{}, fmt.Errorf("load AWS configuration: %w", err)
	}
	return Config{region: region, sdk: sdk}, nil
}

// InstanceID returns the current EC2 instance identifier from IMDS.
func (c Config) InstanceID(ctx context.Context) (string, error) {
	identity, err := imds.NewFromConfig(c.sdk).GetInstanceIdentityDocument(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("read instance identity: %w", err)
	}
	return identity.InstanceID, nil
}

// ObjectStore returns object storage for one S3 bucket; zero disables the read limit.
func (c Config) ObjectStore(bucket string, maxObjectSize int64) *ObjectStore {
	return &ObjectStore{bucket: bucket, region: c.region, maxObjectSize: maxObjectSize, client: s3.NewFromConfig(c.sdk)}
}

// ParameterStore returns an AWS Systems Manager Parameter Store client.
func (c Config) ParameterStore() *ParameterStore {
	parameters := ssm.NewFromConfig(c.sdk)
	return &ParameterStore{reader: parameters, store: parameters}
}

// Secrets returns an AWS Secrets Manager client.
func (c Config) Secrets() *Secrets {
	secrets := secretsmanager.NewFromConfig(c.sdk)
	return &Secrets{reader: secrets, manager: secrets}
}

// Compute returns the AWS infrastructure discovery client.
func (c Config) Compute() *Compute {
	return &Compute{client: ec2.NewFromConfig(c.sdk)}
}
