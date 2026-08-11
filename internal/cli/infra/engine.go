// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/podmin-dev/podmin/internal/cli/config"
	"github.com/podmin-dev/podmin/internal/cli/infra/aws"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// Run writes and executes the module in the inspectable cluster cache.
func Run(ctx context.Context, variables Variables, destroy, autoApprove bool, input io.Reader, output, stderr io.Writer) error {
	command, err := SelectCommand()
	if err != nil {
		return err
	}
	dir, err := WorkDir(variables.ClusterID)
	if err != nil {
		return err
	}
	if destroy {
		if _, err = os.Stat(filepath.Join(dir, "podmin.auto.tfvars.json")); err != nil {
			return fmt.Errorf("generated infrastructure is unavailable; run podmin setup with the cluster's current NodeGroup definitions before teardown: %w", err)
		}
	} else {
		if err = Prepare(variables); err != nil {
			return err
		}
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Dir = dir
		cmd.Stdout = output
		cmd.Stderr = stderr
		cmd.Stdin = input
		return cmd.Run()
	}
	initArgs := []string{"init", "-input=false", "-backend-config=bucket=" + variables.Bucket, "-backend-config=key=tfstate/podmin.tfstate", "-backend-config=region=" + variables.Region, "-backend-config=use_lockfile=true"}
	if variables.Profile != "" {
		initArgs = append(initArgs, "-backend-config=profile="+variables.Profile)
	}
	if err = run(initArgs...); err != nil {
		return fmt.Errorf("initialize OpenTofu/Terraform: %w", err)
	}
	planArgs := []string{"plan", "-input=false", "-out=podmin.plan"}
	if destroy {
		planArgs = append(planArgs, "-destroy")
	}
	if err = run(planArgs...); err != nil {
		return fmt.Errorf("plan OpenTofu/Terraform: %w", err)
	}
	if !autoApprove {
		if _, err = fmt.Fprint(output, "Apply this OpenTofu/Terraform plan? [y/N] "); err != nil {
			return err
		}
		answer, _ := bufio.NewReader(input).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			return errors.New("OpenTofu/Terraform apply cancelled")
		}
	}
	if err = run("apply", "-input=false", "podmin.plan"); err != nil {
		return fmt.Errorf("apply OpenTofu/Terraform plan: %w", err)
	}
	return nil
}

// Prepare replaces a cluster's generated module without executing it.
func Prepare(variables Variables) error {
	dir, err := WorkDir(variables.ClusterID)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	entries, err := fs.ReadDir(aws.Module, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		data, readErr := aws.Module.ReadFile(entry.Name())
		if readErr != nil {
			return readErr
		}
		if err = os.WriteFile(filepath.Join(dir, entry.Name()), data, 0600); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(variables, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "podmin.auto.tfvars.json"), data, 0600)
}

// WorkDir returns the generated infrastructure directory for a cluster.
func WorkDir(clusterID string) (string, error) {
	if !manifest.ValidID(clusterID) {
		return "", fmt.Errorf("invalid cluster ID %q", clusterID)
	}
	cache, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "infrastructure", clusterID), nil
}

// Clean removes generated infrastructure for a disconnected cluster.
func Clean(clusterID string) error {
	dir, err := WorkDir(clusterID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
