// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "github.com/spf13/cobra"

// secretDeleteCommand creates the recoverable deletion command.
func secretDeleteCommand(scope *secretScope) *cobra.Command {
	c := &cobra.Command{Use: "delete <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		if err := validateSecretKey(scope, values[0]); err != nil {
			return err
		}
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		return a.Archive(cmd.Context(), base+"/"+values[0])
	}}
	return c
}
