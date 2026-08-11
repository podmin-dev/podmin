// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// secretDestroyCommand creates the permanent destruction command.
func secretDestroyCommand(scope *secretScope) *cobra.Command {
	var autoApprove bool
	c := &cobra.Command{Use: "destroy <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		if !manifest.ValidID(values[0]) {
			return errors.New("invalid secret key")
		}
		if !autoApprove {
			return errors.New("refusing permanent destruction without --auto-approve")
		}
		return a.Destroy(cmd.Context(), base+"/"+values[0])
	}}
	c.Flags().BoolVarP(&autoApprove, "auto-approve", "y", false, "skip confirmation prompts")
	return c
}
