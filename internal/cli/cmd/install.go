// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/install"
	"github.com/podmin-dev/podmin/internal/cli/tui"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// installCommand creates the managed workload installation command.
func installCommand() *cobra.Command {
	var nodeGroup, provider string
	c := &cobra.Command{Use: "install <component>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "cloudflared" {
			return fmt.Errorf("unknown component %q; supported component: cloudflared", args[0])
		}
		if !manifest.ValidID(nodeGroup) {
			cmd.Root().SilenceUsage = false
			return errors.New("invalid nodegroup ID")
		}
		selected, err := currentContext()
		if err != nil {
			return err
		}
		client, err := loadCloud(cmd.Context(), selected)
		if err != nil {
			return err
		}
		err = tui.Run(cmd.OutOrStdout(), "Installing cloudflared", func(progress tui.Progress) error {
			return install.Cloudflared(cmd.Context(), client, install.Options{Context: selected, NodeGroup: nodeGroup, Provider: provider}, progress)
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Committed cloudflared to NodeGroup %s; nodes will reconcile it asynchronously.\n", nodeGroup)
		return err
	}}
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "nodegroup ID")
	c.Flags().StringVar(&provider, "provider", "", "secret provider (defaults to current context)")
	_ = c.MarkFlagRequired("nodegroup")
	return c
}
