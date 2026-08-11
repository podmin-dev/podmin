// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package dataplane

import (
	"context"
	"net/netip"
)

// platformState is unavailable off Linux.
type platformState struct{}

// activate rejects activation where Linux eBPF is unavailable.
func activate(_ context.Context, _ Tables, _ netip.Prefix, _ <-chan struct{}, _ *platformState) (*platformState, error) {
	return nil, ErrUnsupported
}

// close releases an unavailable platform state.
func (p *platformState) close() error { return nil }

// refresh rejects interface reconciliation where Linux eBPF is unavailable.
func (p *platformState) refresh() error { return ErrUnsupported }
