// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/spf13/cobra"
)

// pullCommand constructs the complete-index image pull command.
func pullCommand() *cobra.Command {
	return &cobra.Command{Use: "pull SOURCE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		root, err := images.CacheRoot()
		if err != nil {
			return err
		}
		return images.Pull(cmd.Context(), args[0], root)
	}}
}
