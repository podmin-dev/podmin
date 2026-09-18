// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "github.com/spf13/cobra"

// secretUpdateCommand creates the secret update command.
func secretUpdateCommand(scope *secretScope) *cobra.Command {
	var filePath string
	var fromStdin bool
	c := &cobra.Command{Use: "update <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		if err := validateSecretKey(scope, values[0]); err != nil {
			return err
		}
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		value, err := readSecret(cmd, "update", values[0], fromStdin, filePath)
		if err != nil {
			return err
		}
		return a.Update(cmd.Context(), base+"/"+values[0], value)
	}}
	c.Flags().BoolVar(&fromStdin, "stdin", false, "Read the secret value from stdin")
	c.Flags().StringVar(&filePath, "file", "", "Read the secret value from a file")
	return c
}
