// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"context"

	"github.com/podplane/ocimage/pkg/store"
)

// Pull imports the complete source image or multi-platform index into cacheRoot.
func Pull(ctx context.Context, source string, cacheRoot string) error {
	ref, err := ParseSource(source)
	if err != nil {
		return err
	}
	st := store.Store{Root: cacheRoot}
	return st.Pull(ctx, ref)
}
