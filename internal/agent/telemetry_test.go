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
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
