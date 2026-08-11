// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package setup

import (
	"context"
	"strings"
	"time"

	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/podmin-dev/podmin/internal/cloud"
)

// cleanupDependencies applies count-and-age retention after successful setup.
func cleanupDependencies(ctx context.Context, store cloud.ObjectStore) error {
	objects, err := store.List(ctx, "dependencies/")
	if err != nil {
		return err
	}
	retention := make([]dependencies.Object, 0, len(objects))
	for _, object := range objects {
		parts := strings.Split(object.Key, "/")
		if len(parts) != 3 {
			continue
		}
		group := parts[1]
		version := parts[2]
		for _, architecture := range []string{"amd64", "arm64"} {
			if strings.Contains(parts[2], "linux-"+architecture) {
				group += "/" + architecture
			}
		}
		retention = append(retention, dependencies.Object{Key: object.Key, Group: group, Version: version, Modified: object.Modified})
	}
	for _, object := range dependencies.Expired(retention, time.Now()) {
		if err = store.Delete(ctx, object.Key); err != nil {
			return err
		}
	}
	return nil
}
