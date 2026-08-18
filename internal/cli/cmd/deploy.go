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
	var service bool
	c := &cobra.Command{Use: "deploy <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !manifest.ValidID(nodeGroup) {
			cmd.Root().SilenceUsage = false
			return errors.New("invalid nodegroup ID")
		}
		var b []byte
		var err error
		overrides := images
		if cmd.Flags().Changed("file") {
			b, err = os.ReadFile(file)
		} else {
			b, err = manifest.Init(args[0], nodeGroup, "default", images, service)
			overrides = nil
		}
		if err != nil {
			return err
		}
		parsed, err := manifest.ParseDeployment(b, overrides, "", args[0], nodeGroup)
		if err != nil {
			return err
		}
		if service && parsed.Service == nil {
			return errors.New("--service requires the manifest to contain a Service")
		}
		name, err := manifest.Name(parsed.Pod)
		if err != nil {
			return err
		}
		if name != args[0] {
			return fmt.Errorf("metadata.name %q does not equal deployment %q", name, args[0])
		}
		if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Deploying %s to NodeGroup %s...\n", args[0], nodeGroup); err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		if err = deploy.Apply(cmd.Context(), a.Objects, nodeGroup, args[0], parsed); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Committed %s to NodeGroup %s; nodes will reconcile it asynchronously.\n", args[0], nodeGroup)
		return err
	}}
	c.Flags().StringVarP(&file, "file", "f", "", "manifest file")
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "nodegroup ID")
	c.Flags().StringArrayVar(&images, "image", nil, "image override")
	c.Flags().BoolVar(&service, "service", false, "include or require a Service (built-in port 443)")
	_ = c.MarkFlagRequired("nodegroup")
	return c
}
