// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// validateCommand creates the manifest validation command.
func validateCommand() *cobra.Command {
	var file string
	var images []string
	c := &cobra.Command{Use: "validate", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		_, err = manifest.ParseDeployment(b, images, "", "", "")
		return err
	}}
	c.Flags().StringVarP(&file, "file", "f", "daemonset.yaml", "manifest file")
	c.Flags().StringArrayVar(&images, "image", nil, "image override")
	return c
}
