// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/cli/deploy"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// deleteCommand creates the deployment deletion command.
func deleteCommand() *cobra.Command {
	var nodeGroup string
	c := &cobra.Command{Use: "delete <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !manifest.ValidID(nodeGroup) || !manifest.ValidID(args[0]) {
			return errors.New("invalid name or nodegroup ID")
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		return deploy.Delete(cmd.Context(), a.Objects, nodeGroup, args[0])
	}}
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "nodegroup ID")
	return c
}
