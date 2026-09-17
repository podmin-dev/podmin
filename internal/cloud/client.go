// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"io"
	"net/netip"
	"time"

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
	PutStream(context.Context, string, io.Reader, int64) error
	PutIfMatch(context.Context, string, []byte, string) error
	List(context.Context, string) ([]ObjectInfo, error)
	Delete(context.Context, string) error
}

// Compute provides the infrastructure discovery needed during setup.
type Compute interface {
	Architecture(context.Context, string) (string, error)
	Image(context.Context, string) (Image, error)
	Network(context.Context, string, netip.Prefix, map[string]string, bool) (Network, error)
	WaitNAT64(context.Context, string, []string, time.Time) error
}

// Image identifies an immutable machine image and its initial kernel.
type Image struct {
	ID             string
	Kernel         string
	RootDeviceName string
}

// Network contains stable subnet allocations and VPC ownership discovered during setup.
type Network struct {
	NodeGroupZones map[string]string
	NodeGroupCIDRs map[string]string
	NAT64CIDRs     map[string]string
	NAT64IPv6CIDRs map[string]string
	ManageVPC      bool
}

// Client contains provider-neutral CLI capabilities.
type Client struct {
	Bucket        Bucket
	Objects       ObjectStore
	SecretStores  map[secrets.Provider]secrets.Manager
	SystemSecrets secrets.Manager
	Compute       Compute
}
