// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/buildvars"
	"github.com/spf13/cobra"
)

// NewRootCommand constructs the complete Podmin command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "podmin",
		Short:         "Run static Pods without a Kubernetes control plane",
		SilenceErrors: true,
		Version:       buildvars.BuildVersion(),
	}
	root.SetVersionTemplate(
		fmt.Sprintf("podmin %s\nbuild date: %s\ncommit: %s\ncommit date: %s\nbranch: %s\n", buildvars.BuildVersion(),
			buildvars.BuildDate(),
			buildvars.CommitHash(),
			buildvars.CommitDate(),
			buildvars.CommitBranch()),
	)
	root.AddCommand(
		connectCommand(),
		useCommand(),
		disconnectCommand(),
		destroyCommand(),
		setupCommand(),
		teardownCommand(),
		fetchCommand(),
		initCommand(),
		validateCommand(),
		deployCommand(),
		deleteCommand(),
		listCommand(),
		installCommand(),
		secretCommand(),
		pullCommand(),
		pushCommand(),
		buildCommand(),
	)
	silenceUsageForRuntimeErrors(root)
	return root
}

// silenceUsageForRuntimeErrors preserves usage for invocation errors only.
func silenceUsageForRuntimeErrors(command *cobra.Command) {
	if command.RunE != nil {
		run := command.RunE
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cmd.Root().SilenceUsage = true
			return run(cmd, args)
		}
	}
	for _, child := range command.Commands() {
		silenceUsageForRuntimeErrors(child)
	}
}
