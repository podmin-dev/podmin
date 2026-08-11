// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/podmin-dev/podmin/internal/cli/config"
)

// ObjectStore gets one object and its opaque version.
type ObjectStore interface {
	Get(context.Context, string) ([]byte, string, error)
}

// Restore recreates generated files after disconnect clears the cache.
func Restore(ctx context.Context, provider ObjectStore, selected config.Context) (Variables, error) {
	variables := Variables{ClusterID: selected.ClusterID, Region: selected.Region, Profile: selected.Profile, Bucket: selected.Bucket}
	directory, err := WorkDir(selected.ClusterID)
	if err != nil {
		return variables, err
	}
	if _, err = os.Stat(filepath.Join(directory, "podmin.auto.tfvars.json")); err == nil {
		return variables, nil
	} else if !os.IsNotExist(err) {
		return variables, err
	}
	body, _, err := provider.Get(ctx, "tfstate/podmin.auto.tfvars.json")
	if err != nil {
		return variables, fmt.Errorf("restore generated infrastructure: %w", err)
	}
	if err = json.Unmarshal(body, &variables); err != nil {
		return variables, err
	}
	variables.Profile = selected.Profile
	if variables.ClusterID != selected.ClusterID || variables.Bucket != selected.Bucket || variables.Region != selected.Region {
		return variables, errors.New("stored infrastructure configuration does not match the current context")
	}
	return variables, Prepare(variables)
}
