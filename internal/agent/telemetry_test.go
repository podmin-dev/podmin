// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/workload"
	"github.com/podmin-dev/podmin/internal/manifest"
)

// fakeNodeServiceIssuer supplies deterministic test identities.
type fakeNodeServiceIssuer struct {
	materials []workload.Material
	revision  uint64
	calls     int
}

// IssueNodeService returns the next test identity.
func (f *fakeNodeServiceIssuer) IssueNodeService(string, string, time.Time) (workload.Material, error) {
	material := f.materials[f.calls]
	f.calls++
	return material, nil
}

// Revision returns the fake authority revision.
func (f *fakeNodeServiceIssuer) Revision() uint64 { return f.revision }

// TestSyncServerCA verifies updates are atomic and failures preserve last-known-good trust.
func TestSyncServerCA(t *testing.T) {
	root := t.TempDir()
	bundle := testServerCACertificate(t, true, 1)
	var readErr error
	readServerCA := func(context.Context) ([]byte, error) { return bundle, readErr }
	changed, err := syncServerCA(context.Background(), readServerCA, root)
	if err != nil || !changed {
		t.Fatalf("initial sync = %t, %v", changed, err)
	}
	target := filepath.Join(root, serverCAFilename)
	first, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(first, bundle) {
		t.Fatalf("initial bundle = %q, %v", first, err)
	}
	if info, statErr := os.Stat(target); statErr != nil {
		t.Fatal(statErr)
	} else if info.Mode().Perm() != 0o644 {
		t.Fatalf("initial mode = %v", info.Mode().Perm())
	}
	if changed, err = syncServerCA(context.Background(), readServerCA, root); err != nil || changed {
		t.Fatalf("unchanged sync = %t, %v", changed, err)
	}
	if err = os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if changed, err = syncServerCA(context.Background(), readServerCA, root); err != nil || !changed {
		t.Fatalf("repair sync = %t, %v", changed, err)
	}
	bundle = testServerCACertificate(t, true, 2)
	if changed, err = syncServerCA(context.Background(), readServerCA, root); err != nil || !changed {
		t.Fatalf("changed sync = %t, %v", changed, err)
	}
	second, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(second, bundle) {
		t.Fatalf("changed bundle = %q, %v", second, err)
	}
	bundle = testServerCACertificate(t, false, 3)
	if replaced, syncErr := syncServerCA(context.Background(), readServerCA, root); syncErr == nil || replaced {
		t.Fatalf("invalid sync = %t, %v", replaced, syncErr)
	}
	if current, fileErr := os.ReadFile(target); fileErr != nil || !bytes.Equal(current, second) {
		t.Fatalf("last-known-good bundle = %q, %v", current, fileErr)
	}
	readErr = errors.New("unavailable")
	if replaced, syncErr := syncServerCA(context.Background(), readServerCA, root); !errors.Is(syncErr, readErr) || replaced {
		t.Fatalf("failed sync = %t, %v", replaced, syncErr)
	}
}

// TestRuntimeRootsAreSiblings preserves separate workload and telemetry ownership.
func TestRuntimeRootsAreSiblings(t *testing.T) {
	if fluentBitTLSRoot == manifest.WorkloadHostRoot || filepath.Dir(fluentBitTLSRoot) != filepath.Dir(manifest.WorkloadHostRoot) {
		t.Fatalf("workload root %q and telemetry root %q are not sibling ownership boundaries", manifest.WorkloadHostRoot, fluentBitTLSRoot)
	}
}

// TestMaintainFluentBitTLSSelfHeals verifies missing identity material is reissued without renewal pressure.
func TestMaintainFluentBitTLSSelfHeals(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	issuer := &fakeNodeServiceIssuer{revision: 7, materials: []workload.Material{testClientIdentity(t, 1, now), testClientIdentity(t, 2, now)}}
	root := testTelemetryRoot(t)
	state := new(fluentBitTLSState)
	restarts := 0
	restart := func(context.Context) error { restarts++; return nil }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if retry := maintainFluentBitTLS(context.Background(), issuer, "node", root, nil, now, state, restart, logger); retry {
		t.Fatal("initial maintenance requested an unexpected retry")
	}
	if issuer.calls != 1 || restarts != 1 || state.pendingRestart {
		t.Fatalf("initial maintenance calls=%d restarts=%d pending=%t", issuer.calls, restarts, state.pendingRestart)
	}
	if retry := maintainFluentBitTLS(context.Background(), issuer, "node", root, nil, now.Add(time.Minute), state, restart, logger); retry {
		t.Fatal("healthy maintenance requested an unexpected retry")
	}
	if issuer.calls != 1 || restarts != 1 {
		t.Fatalf("healthy identity was replaced: calls=%d restarts=%d", issuer.calls, restarts)
	}
	selected := filepath.Join(root, "identity", workload.PrivateKeyFilename)
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if retry := maintainFluentBitTLS(context.Background(), issuer, "node", root, nil, now.Add(2*time.Minute), state, restart, logger); retry {
		t.Fatal("successful repair requested an unexpected retry")
	}
	if issuer.calls != 2 || restarts != 2 || validateClientIdentity(root, state.generation, now.Add(2*time.Minute)) != nil {
		t.Fatalf("identity was not repaired: calls=%d restarts=%d", issuer.calls, restarts)
	}
}

// TestMaintainFluentBitTLSRetainsPendingRestart verifies restart failures do not cause repeated issuance.
func TestMaintainFluentBitTLSRetainsPendingRestart(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	issuer := &fakeNodeServiceIssuer{revision: 1, materials: []workload.Material{testClientIdentity(t, 1, now)}}
	state := new(fluentBitTLSState)
	restarts := 0
	restartError := errors.New("restart failed")
	restart := func(context.Context) error { restarts++; return restartError }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := testTelemetryRoot(t)
	if retry := maintainFluentBitTLS(context.Background(), issuer, "node", root, nil, now, state, restart, logger); !retry {
		t.Fatal("failed restart did not request a retry")
	}
	if retry := maintainFluentBitTLS(context.Background(), issuer, "node", root, nil, now.Add(time.Minute), state, restart, logger); !retry {
		t.Fatal("repeated failed restart did not request a retry")
	}
	if issuer.calls != 1 || restarts != 2 || !state.pendingRestart {
		t.Fatalf("calls=%d restarts=%d pending=%t", issuer.calls, restarts, state.pendingRestart)
	}
}

// TestValidateClientIdentityRejectsUnsafeMaterial verifies selected credentials fail closed.
func TestValidateClientIdentityRejectsUnsafeMaterial(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := map[string]func(*testing.T, string, string){
		"traversing selector": func(t *testing.T, root, _ string) {
			if err := os.Remove(filepath.Join(root, "identity")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../outside", filepath.Join(root, "identity")); err != nil {
				t.Fatal(err)
			}
		},
		"malformed certificate": func(t *testing.T, root, _ string) {
			if err := os.WriteFile(filepath.Join(root, "identity", workload.CertificateFilename), []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked key": func(t *testing.T, root, _ string) {
			key := filepath.Join(root, "identity", workload.PrivateKeyFilename)
			if err := os.Remove(key); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(workload.CertificateFilename, key); err != nil {
				t.Fatal(err)
			}
		},
		"public key mode": func(t *testing.T, root, _ string) {
			if err := os.Chmod(filepath.Join(root, "identity", workload.PrivateKeyFilename), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked generations directory": func(t *testing.T, root, _ string) {
			generations := filepath.Join(root, "identity-generations")
			moved := generations + ".moved"
			if err := os.Rename(generations, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, generations); err != nil {
				t.Fatal(err)
			}
		},
		"foreign certificate": func(t *testing.T, root, _ string) {
			foreign := testClientIdentity(t, 99, now)
			if err := os.WriteFile(filepath.Join(root, "identity", workload.CertificateFilename), foreign.Certificate, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "identity", workload.PrivateKeyFilename), foreign.PrivateKey, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := testTelemetryRoot(t)
			generation, err := installClientIdentity(root, testClientIdentity(t, 1, now))
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, root, generation)
			if err = validateClientIdentity(root, generation, now); err == nil {
				t.Fatal("unsafe identity was accepted")
			}
		})
	}
}

// testTelemetryRoot returns a private, initially absent telemetry root.
func testTelemetryRoot(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "fluent-bit")
}

// testClientIdentity returns a valid matching client certificate and private key.
func testClientIdentity(t *testing.T, serial int64, now time.Time) workload.Material {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "otel-logs"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return workload.Material{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), NotAfter: template.NotAfter}
}

// testServerCACertificate returns a self-signed certificate for server CA tests.
func testServerCACertificate(t *testing.T, ca bool, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: ca, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
