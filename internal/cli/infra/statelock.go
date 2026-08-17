// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var lockIDPattern = regexp.MustCompile(`\bID:\s+([0-9a-fA-F-]+)`)

// LockError reports an existing OpenTofu or Terraform state lock.
type LockError struct {
	ID  string
	err error
}

// Error implements error.
func (e *LockError) Error() string { return "infrastructure state is locked: " + e.ID }

// Unwrap returns the engine error which reported the lock.
func (e *LockError) Unwrap() error { return e.err }

// ForceUnlock removes one confirmed stale state lock.
func ForceUnlock(ctx context.Context, variables Variables, id string, input io.Reader, output, stderr io.Writer) error {
	command, err := SelectCommand()
	if err != nil {
		return err
	}
	dir, err := WorkDir(variables.ClusterID)
	if err != nil {
		return err
	}
	if err = runCommand(ctx, command, dir, os.Environ(), input, output, stderr, "force-unlock", "-force", id); err != nil {
		return fmt.Errorf("force-unlock OpenTofu/Terraform state: %w", err)
	}
	return nil
}

// stateLockID extracts an OpenTofu or Terraform lock identifier.
func stateLockID(diagnostic string) string {
	if !strings.Contains(diagnostic, "Error acquiring the state lock") {
		return ""
	}
	match := lockIDPattern.FindStringSubmatch(diagnostic)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
