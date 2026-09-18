// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "github.com/spf13/cobra"

// secretCreateCommand creates the secret creation command.
func secretCreateCommand(scope *secretScope) *cobra.Command {
	var filePath string
	var fromStdin bool
	c := &cobra.Command{Use: "create <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		if err := validateSecretKey(scope, values[0]); err != nil {
			return err
		}
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		value, err := readSecret(cmd, "create", values[0], fromStdin, filePath)
		if err != nil {
			return err
		}
		return a.Create(cmd.Context(), base+"/"+values[0], value)
	}}
	c.Flags().BoolVar(&fromStdin, "stdin", false, "Read the secret value from stdin")
	c.Flags().StringVar(&filePath, "file", "", "Read the secret value from a file")
	return c
}
