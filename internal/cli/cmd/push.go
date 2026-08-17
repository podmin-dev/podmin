// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/spf13/cobra"
)

// pushCommand constructs the direct-to-object-storage image push command.
func pushCommand() *cobra.Command {
	var pull bool
	c := &cobra.Command{Use: "push SOURCE [DESTINATION]", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		root, err := images.CacheRoot()
		if err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		destination := ""
		if len(args) == 2 {
			destination = args[1]
		}
		var ref string
		err = tui.Run(cmd.OutOrStdout(), "Pushing image", func(progress tui.Progress) error {
			ref, err = images.Push(cmd.Context(), args[0], destination, root, pull, a.Objects, progress)
			return err
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), ref)
		return err
	}}
	c.Flags().BoolVar(&pull, "pull", false, "refresh the source from its remote registry")
	return c
}
