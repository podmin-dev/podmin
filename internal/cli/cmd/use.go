// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/spf13/cobra"
)

// useCommand creates the context selection command.
func useCommand() *cobra.Command {
	return &cobra.Command{Use: "use [cluster-id]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := config.DefaultStore()
		if err != nil {
			return err
		}
		state, err := s.Load()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return writeContexts(cmd.OutOrStdout(), state)
		}
		if _, ok := state.Contexts[args[0]]; !ok {
			return fmt.Errorf("context %q does not exist", args[0])
		}
		state.Current = args[0]
		if err = s.Save(state); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Using context %q.\n", args[0])
		return err
	}}
}

// writeContexts renders every configured context in a stable order.
func writeContexts(output io.Writer, state config.State) error {
	if len(state.Contexts) == 0 {
		_, err := fmt.Fprintln(output, "No contexts.")
		return err
	}
	names := make([]string, 0, len(state.Contexts))
	for name := range state.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "CURRENT\tNAME\tPROVIDER\tREGION\tPROFILE\tBUCKET"); err != nil {
		return err
	}
	for _, name := range names {
		context := state.Contexts[name]
		current := "-"
		if name == state.Current {
			current = "*"
		}
		profile := context.Profile
		if profile == "" {
			profile = "-"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", current, name, context.Provider, context.Region, profile, context.Bucket); err != nil {
			return err
		}
	}
	return writer.Flush()
}
