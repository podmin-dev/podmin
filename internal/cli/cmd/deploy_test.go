// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestDeployCommandsUseNodeGroupFlag verifies the deployment scope flag.
func TestDeployCommandsUseNodeGroupFlag(t *testing.T) {
	for _, command := range []*cobra.Command{initCommand(), deployCommand(), deleteCommand()} {
		flags := command.Flags()
		if flags.Lookup("nodegroup") == nil || flags.ShorthandLookup("g") == nil {
			t.Fatal("nodegroup flag or -g shorthand is missing")
		}
		if flags.Lookup("space") != nil || flags.ShorthandLookup("s") != nil || flags.ShorthandLookup("n") != nil {
			t.Fatal("legacy deployment scope shorthand remains")
		}
	}
}
