// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/spf13/cobra"
)

// teardownCommand destroys compute and networking while retaining the bucket and context.
func teardownCommand() *cobra.Command {
	var autoApprove bool
	c := &cobra.Command{Use: "teardown", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		selected, err := currentContext()
		if err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		variables, err := infra.Restore(cmd.Context(), a.Objects, selected)
		if err != nil {
			return err
		}
		if err = infra.Run(cmd.Context(), variables, true, autoApprove, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return err
		}
		return nil
	}}
	c.Flags().BoolVarP(&autoApprove, "auto-approve", "y", false, "skip confirmation prompts")
	return c
}
