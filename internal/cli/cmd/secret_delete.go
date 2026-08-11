// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// secretDeleteCommand creates the recoverable deletion command.
func secretDeleteCommand(scope *secretScope) *cobra.Command {
	c := &cobra.Command{Use: "delete <key>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, values []string) error {
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		if !manifest.ValidID(values[0]) {
			return errors.New("invalid secret key")
		}
		return a.Archive(cmd.Context(), base+"/"+values[0])
	}}
	return c
}
