// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// EnsureBucket creates an absent bucket, checks its region, and verifies write/read/delete access.
func (s *ObjectStore) EnsureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchBucket" {
			return fmt.Errorf("access bucket: %w", err)
		}
		input := &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}
		if s.region != "us-east-1" {
			input.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(s.region)}
		}
		if _, err = s.client.CreateBucket(ctx, input); err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
		if err = s3.NewBucketExistsWaiter(s.client).Wait(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}, 5*time.Minute); err != nil {
			return err
		}
	}
	loc, err := s.client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("get bucket region: %w", err)
	}
	actual := string(loc.LocationConstraint)
	if actual == "" {
		actual = "us-east-1"
	}
	if actual == "EU" {
		actual = "eu-west-1"
	}
	if actual != s.region {
		return fmt.Errorf("bucket is in %s, not %s", actual, s.region)
	}
	if _, err = s.client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(s.bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("make bucket private: %w", err)
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	key := ".podmin-access-check-" + hex.EncodeToString(random)
	if err = s.Put(ctx, key, []byte("podmin")); err != nil {
		return fmt.Errorf("verify bucket write: %w", err)
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("verify bucket read: %w", err)
	}
	_, readErr := io.Copy(io.Discard, out.Body)
	closeErr := out.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = s.Delete(ctx, key); err != nil {
		return fmt.Errorf("verify bucket delete: %w", err)
	}
	return nil
}

// EmptyAndDeleteBucket permanently removes all current objects and the bucket.
func (s *ObjectStore) EmptyAndDeleteBucket(ctx context.Context) error {
	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket)})
		if err != nil {
			return err
		}
		if len(out.Contents) == 0 {
			break
		}
		objects := make([]types.ObjectIdentifier, 0, len(out.Contents))
		for _, object := range out.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key})
		}
		if err = s.deleteObjects(ctx, objects); err != nil {
			return err
		}
	}
	for {
		out, err := s.client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(s.bucket)})
		if err != nil {
			return err
		}
		objects := make([]types.ObjectIdentifier, 0, len(out.Versions)+len(out.DeleteMarkers))
		for _, object := range out.Versions {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key, VersionId: object.VersionId})
		}
		for _, object := range out.DeleteMarkers {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key, VersionId: object.VersionId})
		}
		if len(objects) == 0 {
			break
		}
		if err = s.deleteObjects(ctx, objects); err != nil {
			return err
		}
	}
	_, err := s.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

// deleteObjects deletes one S3 batch and reports per-object failures.
func (s *ObjectStore) deleteObjects(ctx context.Context, objects []types.ObjectIdentifier) error {
	deleted, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(s.bucket), Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)}})
	if err != nil {
		return err
	}
	if len(deleted.Errors) != 0 {
		return fmt.Errorf("delete object %s: %s", aws.ToString(deleted.Errors[0].Key), aws.ToString(deleted.Errors[0].Message))
	}
	return nil
}
