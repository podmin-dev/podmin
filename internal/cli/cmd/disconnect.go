// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/spf13/cobra"
)

// disconnectCommand creates the local disconnect command.
func disconnectCommand() *cobra.Command {
	return &cobra.Command{Use: "disconnect <cluster-id>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
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
		delete(state.Contexts, args[0])
		if state.Current == args[0] {
			state.Current = ""
		}
		if err := s.Save(state); err != nil {
			return err
		}
		return infra.Clean(args[0])
	}}
}
