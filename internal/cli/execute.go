// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"

	"github.com/podmin-dev/podmin/internal/cli/cmd"
)

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	if err := cmd.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
