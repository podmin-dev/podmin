// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// readSecret reads a secret value from a file, standard input, or an interactive terminal.
func readSecret(cmd *cobra.Command, operation, key string, fromStdin bool, filePath string) ([]byte, error) {
	filePath = strings.TrimSpace(filePath)
	if fromStdin && filePath != "" {
		return nil, errors.New("--stdin and --file are mutually exclusive")
	}
	if filePath != "" {
		value, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read --file: %w", err)
		}
		return validateSecretValue(value)
	}
	if fromStdin {
		value, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return validateSecretValue(value)
	}
	return readInteractiveSecret(cmd, operation, key, os.Stdin)
}

// readInteractiveSecret securely reads a hidden secret from a terminal.
func readInteractiveSecret(cmd *cobra.Command, operation, key string, terminal *os.File) ([]byte, error) {
	fd := int(terminal.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("cannot read secret value interactively: stdin is not a terminal; use --stdin or --file")
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Enter value for %s %q: ", operation, key)
	value, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return nil, fmt.Errorf("read secret value: %w", err)
	}
	return validateSecretValue(value)
}

// validateSecretValue rejects an empty secret value.
func validateSecretValue(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("secret value is empty")
	}
	return value, nil
}
