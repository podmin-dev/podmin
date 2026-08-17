// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/podmin-dev/podmin/internal/agent/identity"
	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/spf13/cobra"
)

// destroyCommand creates the confirmed cluster bucket destruction command.
func destroyCommand() *cobra.Command {
	var autoApprove bool
	c := &cobra.Command{Use: "destroy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := config.DefaultStore()
		if err != nil {
			return err
		}
		selected, err := s.Current()
		if err != nil {
			return err
		}
		if !autoApprove {
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Permanently destroy cluster %q, including its infrastructure, bucket, and certificate authorities? [y/N] ", selected.ClusterID); err != nil {
				return err
			}
			answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				return errors.New("cluster destruction cancelled")
			}
		}
		a, err := loadCloud(cmd.Context(), selected)
		if err != nil {
			return err
		}
		variables, err := infra.Restore(cmd.Context(), a.Objects, selected)
		if err != nil {
			return err
		}
		if err = infra.Run(cmd.Context(), variables, true, true, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return err
		}
		if err = a.SystemSecrets.Destroy(cmd.Context(), "/"+selected.ClusterID+"/_system/workload-ca-key"); err != nil {
			return err
		}
		if err = a.SystemSecrets.Destroy(cmd.Context(), "/"+selected.ClusterID+identity.ClusterCAPathSuffix); err != nil {
			return err
		}
		if err = a.Bucket.EmptyAndDeleteBucket(cmd.Context()); err != nil {
			return err
		}
		state, err := s.Load()
		if err != nil {
			return err
		}
		delete(state.Contexts, selected.ClusterID)
		state.Current = ""
		if err = s.Save(state); err != nil {
			return err
		}
		return infra.Clean(selected.ClusterID)
	}}
	c.Flags().BoolVarP(&autoApprove, "auto-approve", "y", false, "skip confirmation prompts")
	return c
}
