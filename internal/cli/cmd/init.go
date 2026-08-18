// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/spf13/cobra"
)

// initCommand creates the manifest initialization command.
func initCommand() *cobra.Command {
	var file, nodeGroup, namespace string
	var images []string
	var service bool
	c := &cobra.Command{Use: "init <name>", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		b, err := manifest.Init(args[0], nodeGroup, namespace, images, service)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		if _, err = f.Write(b); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}}
	c.Flags().StringVarP(&file, "file", "f", "daemonset.yaml", "manifest file")
	c.Flags().StringVarP(&nodeGroup, "nodegroup", "g", "", "target NodeGroup ID")
	c.Flags().StringVar(&namespace, "namespace", "default", "Kubernetes namespace")
	c.Flags().StringArrayVar(&images, "image", nil, "container image")
	c.Flags().BoolVar(&service, "service", false, "include a default TCP Service on port 8080")
	return c
}
