// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/podmin-dev/podmin/internal/buildvars"
	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/dependencies"
	"github.com/spf13/cobra"
)

// fetchCommand downloads and verifies the host architecture dependency set.
func fetchCommand() *cobra.Command {
	var agentSource string
	c := &cobra.Command{Use: "fetch", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cache, err := config.CacheDir()
		if err != nil {
			return err
		}
		fetcher := dependencies.Fetcher{CacheDir: filepath.Join(cache, "dependencies"), SourceDir: agentSource, AgentVersion: buildvars.BuildVersion()}
		artifacts, err := fetcher.Fetch(cmd.Context(), runtime.GOARCH)
		if err != nil {
			return err
		}
		for _, artifact := range artifacts {
			if _, err = fmt.Fprintln(cmd.OutOrStdout(), artifact.Path); err != nil {
				return err
			}
		}
		return nil
	}}
	c.Flags().StringVar(&agentSource, "agent-source", "", "build podmin-agent from this source checkout")
	return c
}
