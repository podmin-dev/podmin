// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/podmin-dev/podmin/internal/cli/deploy"
	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// deployCommand creates the deployment command.
func deployCommand() *cobra.Command {
	var file, nodeGroup string
	var images []string
	c := &cobra.Command{Use: "deploy <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !manifest.ValidID(nodeGroup) {
			return errors.New("invalid nodegroup ID")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		parsed, err := manifest.ParseDeployment(b, images, "", args[0], nodeGroup)
		if err != nil {
			return err
		}
		name, err := manifest.Name(parsed.Pod)
		if err != nil {
			return err
		}
		if name != args[0] {
			return fmt.Errorf("metadata.name %q does not equal deployment %q", name, args[0])
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		return deploy.Apply(cmd.Context(), a.Objects, nodeGroup, args[0], parsed)
	}}
	c.Flags().StringVarP(&file, "file", "f", "daemonset.yaml", "manifest file")
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "nodegroup ID")
	c.Flags().StringArrayVar(&images, "image", nil, "image override")
	return c
}
