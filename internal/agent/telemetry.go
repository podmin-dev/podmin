// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
	"golang.org/x/sys/unix"
)

// Keep host telemetry credentials outside the workload subtree, which is
// garbage-collected exclusively by the static-Pod reconciler.
const fluentBitTLSRoot = "/run/podmin/telemetry"
const serverCAFilename = "server-ca.pem"
const maximumIdentityFileSize = 64 << 10

// nodeServiceIssuer issues short-lived identities from the current workload CA.
type nodeServiceIssuer interface {
	IssueNodeService(string, string, time.Time) (workload.Material, error)
	Revision() uint64
}

// fluentBitTLSState is non-secret process state for the installed identity.
type fluentBitTLSState struct {
	notAfter          time.Time
	authorityRevision uint64
	generation        string
	pendingRestart    bool
}

// runFluentBitTLS maintains Fluent Bit client identity and server trust material.
func runFluentBitTLS(ctx context.Context, authority *workload.Authority, nodeID, root string, readServerCA func(context.Context) ([]byte, error), logger *slog.Logger) error {
	state := new(fluentBitTLSState)
	var delay time.Duration
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		retrySoon := maintainFluentBitTLS(ctx, authority, nodeID, root, readServerCA, time.Now(), state, restartFluentBit, logger)
		delay = 5 * time.Minute
		if retrySoon {
			delay = 5 * time.Second
		}
	}
}

// restartFluentBit starts or restarts the collector without overriding a mask.
func restartFluentBit(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", "fluent-bit.service").Run()
}

// maintainFluentBitTLS performs one identity, trust, and service maintenance pass.
func maintainFluentBitTLS(ctx context.Context, authority nodeServiceIssuer, nodeID, root string, readServerCA func(context.Context) ([]byte, error), now time.Time, state *fluentBitTLSState, restart func(context.Context) error, logger *slog.Logger) bool {
	retrySoon := false
	identityValid := authority == nil || validateClientIdentity(root, state.generation, now) == nil
	if authority != nil && (!identityValid || workload.NeedsRenewal(state.notAfter, now) || state.authorityRevision != authority.Revision()) {
		revision := authority.Revision()
		material, err := authority.IssueNodeService("otel-logs", nodeID, now)
		if err != nil {
			logger.Warn("issue Fluent Bit identity", "error", err)
			retrySoon = true
		} else if authority.Revision() != revision {
			logger.Warn("discard Fluent Bit identity after workload CA changed")
			retrySoon = true
		} else {
			generation, installErr := installClientIdentity(root, material)
			if installErr != nil {
				logger.Warn("install Fluent Bit client identity", "error", installErr)
				retrySoon = true
			} else {
				state.notAfter = material.NotAfter
				state.authorityRevision = revision
				state.generation = generation
				state.pendingRestart = true
			}
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
			state.pendingRestart = state.pendingRestart || changed
		}
	}
	if state.pendingRestart {
		if err := restart(ctx); err != nil {
			logger.Warn("restart Fluent Bit after TLS material changed", "error", err)
			retrySoon = true
		} else {
			state.pendingRestart = false
		}
	}
	return retrySoon
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

// installClientIdentity atomically selects one new complete certificate generation.
func installClientIdentity(root string, material workload.Material) (string, error) {
	digest := sha512.Sum512(material.Certificate)
	generation := hex.EncodeToString(digest[:])
	if err := ensurePrivateDirectory(filepath.Dir(root)); err != nil {
		return "", err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return "", err
	}
	generations := filepath.Join(root, "identity-generations")
	if err := ensurePrivateDirectory(generations); err != nil {
		return "", err
	}
	directory := filepath.Join(generations, generation)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", fmt.Errorf("create identity generation: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	for name, value := range map[string][]byte{workload.CertificateFilename: material.Certificate, workload.PrivateKeyFilename: material.PrivateKey} {
		if err := writeExclusiveFile(filepath.Join(directory, name), value); err != nil {
			return "", err
		}
	}
	if err := validateGeneration(directory, generation, time.Now()); err != nil {
		return "", fmt.Errorf("validate new identity generation: %w", err)
	}
	selected := filepath.Join(root, "identity")
	previous, _ := os.Readlink(selected)
	next := selected + ".next"
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(filepath.Join("identity-generations", generation), next); err != nil {
		return "", err
	}
	if err := os.Rename(next, selected); err != nil {
		return "", err
	}
	complete = true
	entries, err := os.ReadDir(generations)
	if err != nil {
		return generation, err
	}
	current := filepath.Join("identity-generations", generation)
	for _, entry := range entries {
		target := filepath.Join("identity-generations", entry.Name())
		if entry.IsDir() && target != current && target != previous {
			if err = os.RemoveAll(filepath.Join(generations, entry.Name())); err != nil {
				return generation, err
			}
		}
	}
	return generation, nil
}

// validateClientIdentity verifies the selected generation is exactly the one installed by this process.
func validateClientIdentity(root, generation string, now time.Time) error {
	if len(generation) != sha512.Size*2 {
		return errors.New("no expected identity generation")
	}
	if err := validatePrivateDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	if err := validatePrivateDirectory(root); err != nil {
		return err
	}
	if err := validatePrivateDirectory(filepath.Join(root, "identity-generations")); err != nil {
		return err
	}
	selected := filepath.Join(root, "identity")
	info, err := os.Lstat(selected)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return errors.New("identity selector is not a symlink")
	}
	target, err := os.Readlink(selected)
	if err != nil {
		return err
	}
	expected := filepath.Join("identity-generations", generation)
	if target != expected || filepath.IsAbs(target) || strings.Contains(target, "..") {
		return errors.New("identity selector does not select the expected generation")
	}
	return validateGeneration(filepath.Join(root, expected), generation, now)
}

// validateGeneration verifies private files from one pinned, non-symlinked generation directory.
func validateGeneration(directory, generation string, now time.Time) error {
	if err := validatePrivateDirectory(directory); err != nil {
		return err
	}
	certificate, err := readPrivateRegularFile(filepath.Join(directory, workload.CertificateFilename))
	if err != nil {
		return err
	}
	key, err := readPrivateRegularFile(filepath.Join(directory, workload.PrivateKeyFilename))
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return fmt.Errorf("parse identity key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse identity certificate: %w", err)
	}
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("identity certificate is not currently valid")
	}
	digest := sha512.Sum512(certificate)
	if hex.EncodeToString(digest[:]) != generation {
		return errors.New("identity certificate does not match expected generation")
	}
	return nil
}

// ensurePrivateDirectory creates or validates a directory owned by this process with mode 0700.
func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validatePrivateDirectory(path)
}

// validatePrivateDirectory rejects symlinks and directories with unsafe ownership or permissions.
func validatePrivateDirectory(path string) error {
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR || status.Mode&0o777 != 0o700 || int(status.Uid) != os.Geteuid() {
		return fmt.Errorf("unsafe private directory %q", path)
	}
	return nil
}

// writeExclusiveFile writes one immutable identity file without following an existing path.
func writeExclusiveFile(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(value); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// readPrivateRegularFile opens and validates one bounded identity file without following symlinks.
func readPrivateRegularFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	var status unix.Stat_t
	if err = unix.Fstat(fd, &status); err != nil {
		return nil, err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o777 != 0o600 || int(status.Uid) != os.Geteuid() || status.Nlink != 1 || status.Size <= 0 || status.Size > maximumIdentityFileSize {
		return nil, fmt.Errorf("unsafe identity file %q", path)
	}
	value, err := io.ReadAll(io.LimitReader(file, maximumIdentityFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(value) > maximumIdentityFileSize {
		return nil, fmt.Errorf("identity file %q is too large", path)
	}
	return value, nil
}
