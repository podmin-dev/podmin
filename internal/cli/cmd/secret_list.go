// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// secretListCommand creates the secret listing command.
func secretListCommand(scope *secretScope) *cobra.Command {
	c := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		a, base, err := secretTarget(cmd, scope)
		if err != nil {
			return err
		}
		names, err := a.List(cmd.Context(), base)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(names, "\n"))
		return err
	}}
	return c
}
