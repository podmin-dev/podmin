// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cloud"
	awscloud "github.com/podmin-dev/podmin/internal/cloud/aws"
	"github.com/spf13/cobra"
)

// currentContext loads the selected CLI context.
func currentContext() (config.Context, error) {
	s, err := config.DefaultStore()
	if err != nil {
		return config.Context{}, err
	}
	return s.Current()
}

// currentCloud constructs provider capabilities for the selected context.
func currentCloud(cmd *cobra.Command) (*cloud.Client, error) {
	c, err := currentContext()
	if err != nil {
		return nil, err
	}
	return loadCloud(cmd.Context(), c)
}

// loadCloud constructs provider capabilities for a context.
func loadCloud(ctx context.Context, c config.Context) (*cloud.Client, error) {
	switch c.Provider {
	case "aws":
		return awscloud.New(ctx, c.Region, c.Profile, c.Bucket)
	default:
		return nil, fmt.Errorf("unsupported cloud provider %q", c.Provider)
	}
}
