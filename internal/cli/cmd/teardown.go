// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/podmin-dev/podmin/internal/cli/infra"
	"github.com/spf13/cobra"
)

// teardownCommand destroys compute and networking while retaining the bucket and context.
func teardownCommand() *cobra.Command {
	var autoApprove bool
	c := &cobra.Command{Use: "teardown", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		selected, err := currentContext()
		if err != nil {
			return err
		}
		a, err := currentCloud(cmd)
		if err != nil {
			return err
		}
		variables, err := infra.Restore(cmd.Context(), a.Objects, selected)
		if err != nil {
			return err
		}
		if err = infra.Run(cmd.Context(), variables, true, autoApprove, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			var lock *infra.LockError
			if !errors.As(err, &lock) {
				return err
			}
			active, processErr := infra.ActiveCommands(cmd.Context())
			if processErr != nil {
				return fmt.Errorf("state lock %s may be stale, but local OpenTofu/Terraform processes could not be checked: %w", lock.ID, processErr)
			}
			if len(active) > 0 {
				return fmt.Errorf("state lock %s is held while OpenTofu/Terraform is running locally (%s); stop that operation before retrying", lock.ID, strings.Join(active, ", "))
			}
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "State lock %s appears stale; no local OpenTofu/Terraform process is running. Force-unlock it and retry teardown? [y/N] ", lock.ID); err != nil {
				return err
			}
			answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				return fmt.Errorf("infrastructure state remains locked: %s", lock.ID)
			}
			if err = infra.ForceUnlock(cmd.Context(), variables, lock.ID, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
			if err = infra.Run(cmd.Context(), variables, true, autoApprove, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
		}
		return nil
	}}
	c.Flags().BoolVarP(&autoApprove, "auto-approve", "y", false, "skip confirmation prompts")
	return c
}
