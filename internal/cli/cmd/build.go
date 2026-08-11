// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"runtime"

	"github.com/podmin-dev/podmin/internal/cli/images"
	"github.com/podplane/ocimage/pkg/build"
	"github.com/spf13/cobra"
)

// buildCommand constructs the ocimage-backed local OCI build command.
func buildCommand() *cobra.Command {
	var tags, platforms []string
	var file string
	var pull bool
	c := &cobra.Command{Use: "build [PATH]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		root, err := images.CacheRoot()
		if err != nil {
			return err
		}
		contextDir := "."
		if len(args) == 1 {
			contextDir = args[0]
		}
		if len(platforms) == 0 {
			platforms = []string{"linux/" + runtime.GOARCH}
		}
		_, err = build.Build(cmd.Context(), build.Options{ContextDir: contextDir, File: file, Tags: tags, Platforms: platforms, StoreRoot: root, Pull: pull})
		return err
	}}
	c.Flags().StringArrayVarP(&tags, "tag", "t", nil, "image tag (repeatable)")
	c.Flags().StringArrayVar(&platforms, "platform", nil, "target platform (repeatable)")
	c.Flags().StringVarP(&file, "file", "f", "", "Containerfile or Dockerfile")
	c.Flags().BoolVar(&pull, "pull", false, "refresh base images")
	_ = c.MarkFlagRequired("tag")
	return c
}
