// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"net/netip"

	"github.com/podmin-dev/podmin/internal/secrets"
)

// Bucket manages the object-storage container owned by a cluster.
type Bucket interface {
	EnsureBucket(context.Context) error
	EmptyAndDeleteBucket(context.Context) error
}

// ObjectStore provides the object operations used by the CLI.
type ObjectStore interface {
	Get(context.Context, string) ([]byte, string, error)
	Put(context.Context, string, []byte) error
	PutAbsent(context.Context, string, []byte, map[string]string) error
	PutIfMatch(context.Context, string, []byte, string) error
	List(context.Context, string) ([]ObjectInfo, error)
	Delete(context.Context, string) error
}

// Compute provides the infrastructure discovery needed during setup.
type Compute interface {
	Architecture(context.Context, string) (string, error)
	SubnetCIDRs(context.Context, string, netip.Prefix, []string) (map[string]string, error)
}

// Client contains provider-neutral CLI capabilities.
type Client struct {
	Bucket        Bucket
	Objects       ObjectStore
	SecretStores  map[secrets.Provider]secrets.Manager
	SystemSecrets secrets.Manager
	Compute       Compute
}
