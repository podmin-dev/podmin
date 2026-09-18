// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
)

const telemetryIdentityRoot = "/run/podmin/fluent-bit"

// runTelemetryIdentity maintains a renewable Fluent Bit client identity.
func runTelemetryIdentity(ctx context.Context, authority *workload.Authority, nodeID, root string, logger *slog.Logger) error {
	var notAfter time.Time
	var revision uint64
	delay := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		now := time.Now()
		if !workload.NeedsRenewal(notAfter, now) && revision == authority.Revision() {
			delay = 5 * time.Minute
			continue
		}
		material, err := authority.IssueNodeService("otel-logs", nodeID, now)
		if err != nil {
			logger.Warn("issue Fluent Bit identity", "error", err)
			delay = 5 * time.Second
			continue
		}
		if err = publishTelemetryIdentity(root, material); err != nil {
			return fmt.Errorf("publish Fluent Bit identity: %w", err)
		}
		if err = exec.CommandContext(ctx, "systemctl", "try-restart", "fluent-bit.service").Run(); err != nil {
			logger.Warn("restart Fluent Bit after identity renewal", "error", err)
			delay = 5 * time.Second
			continue
		}
		notAfter = material.NotAfter
		revision = authority.Revision()
		delay = 5 * time.Minute
	}
}

// publishTelemetryIdentity atomically selects one complete certificate generation.
func publishTelemetryIdentity(root string, material workload.Material) error {
	digest := sha512.Sum512(material.Certificate)
	generation := hex.EncodeToString(digest[:])
	generations := filepath.Join(root, "identity-generations")
	directory := filepath.Join(generations, generation)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	for name, value := range map[string][]byte{workload.CertificateFilename: material.Certificate, workload.PrivateKeyFilename: material.PrivateKey} {
		if err := os.WriteFile(filepath.Join(directory, name), value, 0o600); err != nil {
			return err
		}
	}
	selected := filepath.Join(root, "identity")
	previous, _ := os.Readlink(selected)
	next := selected + ".next"
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(filepath.Join("identity-generations", generation), next); err != nil {
		return err
	}
	if err := os.Rename(next, selected); err != nil {
		return err
	}
	entries, err := os.ReadDir(generations)
	if err != nil {
		return err
	}
	current := filepath.Join("identity-generations", generation)
	for _, entry := range entries {
		target := filepath.Join("identity-generations", entry.Name())
		if entry.IsDir() && target != current && target != previous {
			if err = os.RemoveAll(filepath.Join(generations, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
