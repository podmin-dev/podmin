// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// validateCommand creates the manifest validation command.
func validateCommand() *cobra.Command {
	var file string
	var images []string
	var service bool
	c := &cobra.Command{Use: "validate", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		deployment, err := manifest.ParseDeployment(b, images, "", "", "")
		if err != nil {
			return err
		}
		if service && deployment.Service == nil {
			return errors.New("--service requires the manifest to contain a Service")
		}
		return nil
	}}
	c.Flags().StringVarP(&file, "file", "f", "", "manifest file")
	c.Flags().StringArrayVar(&images, "image", nil, "image override")
	c.Flags().BoolVar(&service, "service", false, "require the manifest to contain a Service")
	_ = c.MarkFlagRequired("file")
	return c
}
