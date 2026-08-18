// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/podmin-dev/podmin/internal/cli/deploy"
	"github.com/spf13/cobra"
)

// listCommand creates the committed desired-state inventory command.
func listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		listings, err := deploy.List(cmd.Context(), client.Objects)
		if err != nil {
			return err
		}
		return writeListings(cmd.OutOrStdout(), listings)
	}}
}

// writeListings renders the stable human-readable desired-state table.
func writeListings(output io.Writer, listings []deploy.Listing) error {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tNAMESPACE\tNODEGROUP\tSERVICE\tORIGIN"); err != nil {
		return err
	}
	for _, listing := range listings {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", listing.Name, listing.Namespace, listing.NodeGroup, listing.Service, listing.Origin); err != nil {
			return err
		}
	}
	return writer.Flush()
}
