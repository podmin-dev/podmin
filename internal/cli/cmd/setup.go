// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/setup"
	"github.com/spf13/cobra"
)

// setupCommand creates or updates AWS compute and networking.
func setupCommand() *cobra.Command {
	var cidr string
	var values []string
	var agentSource string
	var autoApprove bool
	c := &cobra.Command{Use: "setup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		selected, err := currentContext()
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintln(cmd.OutOrStdout(), "Loading cloud configuration..."); err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		return setup.Run(cmd.Context(), a, setup.Options{Context: selected, VPCCIDR: cidr, NodeGroups: values, AgentSource: agentSource, AutoApprove: autoApprove, Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr()})
	}}
	c.Flags().StringVar(&cidr, "vpc-cidr", "", "private IPv4 VPC CIDR")
	c.Flags().StringArrayVar(&values, "nodegroup", nil, "authoritative NodeGroup definition")
	c.Flags().StringVar(&agentSource, "agent-source", "", "build podmin-agent from this source checkout")
	c.Flags().BoolVarP(&autoApprove, "auto-approve", "y", false, "skip confirmation prompts")
	_ = c.MarkFlagRequired("vpc-cidr")
	return c
}
