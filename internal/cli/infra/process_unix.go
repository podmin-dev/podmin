// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

//go:build darwin || linux

package infra

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ActiveCommands returns locally running OpenTofu and Terraform executables.
func ActiveCommands(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "comm=").Output()
	if err != nil {
		return nil, err
	}
	var active []string
	for _, command := range strings.Fields(string(output)) {
		name := filepath.Base(command)
		if name == "tofu" || name == "terraform" {
			active = append(active, command)
		}
	}
	sort.Strings(active)
	return active, nil
}

// configureCommand gives OpenTofu or Terraform time to persist state and release its lock.
func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGINT)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 30 * time.Second
}
