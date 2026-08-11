// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/podmin-dev/podmin/internal/cloud"
)

// ObjectStore provides object operations for one S3 bucket.
type ObjectStore struct {
	bucket, region string
	maxObjectSize  int64
	client         *s3.Client
}

// Put replaces an object.
func (s *ObjectStore) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	return err
}

// PutAbsent creates an object only when its key is unused.
func (s *ObjectStore) PutAbsent(ctx context.Context, key string, body []byte, metadata map[string]string) error {
	for attempt := 0; ; attempt++ {
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body), IfNoneMatch: aws.String("*"), Metadata: metadata})
		switch apiErrorCode(err) {
		case "PreconditionFailed":
			return cloud.ErrExists
		case "ConditionalRequestConflict":
			if attempt < 2 {
				continue
			}
		}
		return err
	}
}

// List returns one paginated listing beneath prefix.
func (s *ObjectStore) List(ctx context.Context, prefix string) ([]cloud.ObjectInfo, error) {
	var result []cloud.ObjectInfo
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, object := range out.Contents {
			result = append(result, cloud.ObjectInfo{Key: aws.ToString(object.Key), Modified: aws.ToTime(object.LastModified)})
		}
		if !aws.ToBool(out.IsTruncated) {
			return result, nil
		}
		token = out.NextContinuationToken
	}
}

// Get returns an object's bytes and ETag for a later conditional write.
func (s *ObjectStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	return s.getBounded(ctx, key, s.maxObjectSize)
}

// getBounded returns an object's bytes and ETag, optionally enforcing a size limit.
func (s *ObjectStore) getBounded(ctx context.Context, key string, limit int64) ([]byte, string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing *types.NoSuchKey
		var api smithy.APIError
		if errors.As(err, &missing) || (errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound")) {
			return nil, "", cloud.ErrNotFound
		}
		return nil, "", err
	}
	defer func() { _ = out.Body.Close() }()
	var body []byte
	if limit == 0 {
		body, err = io.ReadAll(out.Body)
	} else {
		body, err = readBounded(out.Body, limit)
	}
	return body, aws.ToString(out.ETag), err
}

// PutIfMatch replaces an object matching version, or creates it when version is empty.
func (s *ObjectStore) PutIfMatch(ctx context.Context, key string, body []byte, version string) error {
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body)}
	if version != "" {
		input.IfMatch = aws.String(version)
	} else {
		input.IfNoneMatch = aws.String("*")
	}
	_, err := s.client.PutObject(ctx, input)
	if code := apiErrorCode(err); code == "PreconditionFailed" || code == "ConditionalRequestConflict" {
		return cloud.ErrPrecondition
	}
	return err
}

// Delete removes an object.
func (s *ObjectStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

// apiErrorCode returns the Smithy API error code, if present.
func apiErrorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

// readBounded reads an object while rejecting unexpectedly large control data.
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("object exceeds %d bytes", limit)
	}
	return body, nil
}
