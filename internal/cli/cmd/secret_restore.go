// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// secretRestoreCommand creates the restoration command.
func secretRestoreCommand(scope *secretScope) *cobra.Command {
	c := &cobra.Command{Use: "restore <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		if !manifest.ValidID(values[0]) {
			return errors.New("invalid secret key")
		}
		return a.Restore(cmd.Context(), base+"/"+values[0])
	}}
	return c
}
