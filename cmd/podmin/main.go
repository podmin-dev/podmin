// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/podmin-dev/podmin/internal/cli"
)

// main runs the Podmin CLI.
func main() {
	os.Exit(cli.Execute())
}
