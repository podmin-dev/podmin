// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// secretUpdateCommand creates the secret update command.
func secretUpdateCommand(scope *secretScope) *cobra.Command {
	var filePath string
	var fromStdin bool
	c := &cobra.Command{Use: "update <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		if !manifest.ValidID(values[0]) {
			return errors.New("invalid secret key")
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
