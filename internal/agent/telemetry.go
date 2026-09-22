// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
)

const fluentBitTLSRoot = "/run/podmin/fluent-bit"
const serverCAFilename = "server-ca.pem"

// runFluentBitTLS maintains Fluent Bit client identity and server trust material.
func runFluentBitTLS(ctx context.Context, authority *workload.Authority, nodeID, root string, readServerCA func(context.Context) ([]byte, error), logger *slog.Logger) error {
	var notAfter time.Time
	var authorityRevision uint64
	var pendingRestart bool
	var delay time.Duration
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		now := time.Now()
		retrySoon := false
		if authority != nil && (workload.NeedsRenewal(notAfter, now) || authorityRevision != authority.Revision()) {
			material, err := authority.IssueNodeService("otel-logs", nodeID, now)
			if err != nil {
				logger.Warn("issue Fluent Bit identity", "error", err)
				retrySoon = true
			} else {
				if err = installClientIdentity(root, material); err != nil {
					return fmt.Errorf("install Fluent Bit client identity: %w", err)
				}
				notAfter = material.NotAfter
				authorityRevision = authority.Revision()
				pendingRestart = true
			}
		}
		if readServerCA != nil {
			changed, err := syncServerCA(ctx, readServerCA, root)
			if err != nil {
				logger.Warn("sync Fluent Bit server CA", "error", err)
				if _, statErr := os.Stat(filepath.Join(root, serverCAFilename)); errors.Is(statErr, os.ErrNotExist) {
					retrySoon = true
				}
			} else {
				pendingRestart = pendingRestart || changed
			}
		}
		if pendingRestart {
			if err := exec.CommandContext(ctx, "systemctl", "try-restart", "fluent-bit.service").Run(); err != nil {
				logger.Warn("restart Fluent Bit after TLS material changed", "error", err)
				retrySoon = true
			} else {
				pendingRestart = false
			}
		}
		delay = 5 * time.Minute
		if retrySoon {
			delay = 5 * time.Second
		}
	}
}

// syncServerCA retrieves and atomically installs a changed server CA bundle.
func syncServerCA(ctx context.Context, readServerCA func(context.Context) ([]byte, error), root string) (bool, error) {
	bundle, err := readServerCA(ctx)
	if err != nil {
		return false, err
	}
	return installServerCA(root, bundle)
}

// installServerCA validates and atomically installs a CA-only PEM bundle.
func installServerCA(root string, bundle []byte) (bool, error) {
	remaining := bytes.TrimSpace(bundle)
	certificates := 0
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return false, errors.New("server CA bundle contains non-certificate data")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return false, errors.New("server CA bundle is malformed")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return false, fmt.Errorf("parse server CA certificate: %w", err)
		}
		if !certificate.BasicConstraintsValid || !certificate.IsCA {
			return false, errors.New("server CA bundle contains a non-CA certificate")
		}
		certificates++
		remaining = bytes.TrimSpace(rest)
	}
	if certificates == 0 {
		return false, errors.New("server CA bundle contains no certificates")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return false, err
	}
	target := filepath.Join(root, serverCAFilename)
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, bundle) {
		return false, nil
	}
	temporary, err := os.CreateTemp(root, ".server-ca-*")
	if err != nil {
		return false, err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temporary.Chmod(0o644); err == nil {
		_, err = temporary.Write(bundle)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	if err = os.Rename(name, target); err != nil {
		return false, err
	}
	return true, nil
}

// installClientIdentity atomically selects one complete certificate generation.
func installClientIdentity(root string, material workload.Material) error {
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
