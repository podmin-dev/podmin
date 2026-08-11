// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/spf13/cobra"
)

// useCommand creates the context selection command.
func useCommand() *cobra.Command {
	return &cobra.Command{Use: "use <cluster-id>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := config.DefaultStore()
		if err != nil {
			return err
		}
		state, err := s.Load()
		if err != nil {
			return err
		}
		if _, ok := state.Contexts[args[0]]; !ok {
			return fmt.Errorf("context %q does not exist", args[0])
		}
		state.Current = args[0]
		return s.Save(state)
	}}
}
