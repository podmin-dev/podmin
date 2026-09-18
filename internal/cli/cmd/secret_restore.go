// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "github.com/spf13/cobra"

// secretRestoreCommand creates the restoration command.
func secretRestoreCommand(scope *secretScope) *cobra.Command {
	c := &cobra.Command{Use: "restore <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		if err := validateSecretKey(scope, values[0]); err != nil {
			return err
		}
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		return a.Restore(cmd.Context(), base+"/"+values[0])
	}}
	return c
}
