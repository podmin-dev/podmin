// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package workload

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/podplane/s3lect"
)

// fakeStorage is an in-memory conditional object store.
type fakeStorage struct {
	mu       sync.Mutex
	body     []byte
	revision int
	conflict bool
}

// Get retrieves the fake CA object.
func (s *fakeStorage) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != CAStateKey || s.body == nil {
		return nil, "", s3lect.ErrStorageNotFound
	}
	return append([]byte(nil), s.body...), fmt.Sprint(s.revision), nil
}

// PutIfMatch conditionally writes the fake CA object.
func (s *fakeStorage) PutIfMatch(_ context.Context, key string, body []byte, etag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conflict {
		s.conflict = false
		return s3lect.ErrStoragePrecondition
	}
	want := ""
	if s.body != nil {
		want = fmt.Sprint(s.revision)
	}
	if key != CAStateKey || etag != want {
		return s3lect.ErrStoragePrecondition
	}
	s.body = append([]byte(nil), body...)
	s.revision++
	return nil
}

// TestEnsureBootstrapCASAndNoop verifies bootstrap retries and stable no-op state.
func TestEnsureBootstrapCASAndNoop(t *testing.T) {
	storage := &fakeStorage{conflict: true}
	a := testAuthority(t, storage)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := a.Ensure(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), storage.body...)
	if storage.revision != 1 {
		t.Fatalf("writes = %d, want 1", storage.revision)
	}
	if err := a.Ensure(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if storage.revision != 1 || !bytes.Equal(first, storage.body) {
		t.Fatal("no-op ensure wrote state")
	}
}

// TestRotationAndPruning verifies two-phase overlap, promotion, and expiry pruning.
func TestRotationAndPruning(t *testing.T) {
	storage := new(fakeStorage)
	a := testAuthority(t, storage)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.Ensure(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	rotate := start.AddDate(1, 0, 0).Add(-30 * 24 * time.Hour)
	if err := a.Ensure(context.Background(), rotate); err != nil {
		t.Fatal(err)
	}
	state := decodeTestState(t, storage.body)
	if state.Current != 1 || len(state.Certs) != 2 || state.Certs[1].ActivateAt == nil || state.Certs[0].RetiredAt != nil {
		t.Fatalf("unexpected pending state: %+v", state)
	}
	first, err := x509.ParseCertificate(state.Certs[0].DER)
	if err != nil {
		t.Fatal(err)
	}
	second, err := x509.ParseCertificate(state.Certs[1].DER)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.RawSubjectPublicKeyInfo, second.RawSubjectPublicKeyInfo) {
		t.Fatal("CA certificate rotation changed the CA key")
	}
	if err := a.Sync(context.Background(), rotate); err != nil {
		t.Fatal(err)
	}
	if a.ca.SerialNumber.Uint64() != 1 || countPEMCertificates(a.bundle) != 2 {
		t.Fatal("phase one changed issuer or omitted a root")
	}
	if err := a.Ensure(context.Background(), rotate.Add(activationLag)); err != nil {
		t.Fatal(err)
	}
	state = decodeTestState(t, storage.body)
	if state.Current != 2 || state.Certs[0].RetiredAt == nil || state.Certs[1].ActivateAt != nil {
		t.Fatalf("pending CA not promoted: %+v", state)
	}
	if err := a.Ensure(context.Background(), rotate.Add(activationLag+retention)); err != nil {
		t.Fatal(err)
	}
	if len(decodeTestState(t, storage.body).Certs) != 2 {
		t.Fatal("previous CA pruned before skew allowance")
	}
	if err := a.Ensure(context.Background(), rotate.Add(activationLag+retention+clockSkew)); err != nil {
		t.Fatal(err)
	}
	state = decodeTestState(t, storage.body)
	if len(state.Certs) != 1 || state.Certs[0].Generation != 2 {
		t.Fatalf("previous CA not pruned: %+v", state)
	}
}

// TestExpiredCurrentRecovery verifies immediate recovery with and without a pending CA.
func TestExpiredCurrentRecovery(t *testing.T) {
	for _, pending := range []bool{false, true} {
		storage := new(fakeStorage)
		a := testAuthority(t, storage)
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := a.Ensure(context.Background(), start); err != nil {
			t.Fatal(err)
		}
		if pending {
			if err := a.Ensure(context.Background(), start.AddDate(1, 0, 0).Add(-retention)); err != nil {
				t.Fatal(err)
			}
		}
		expired := start.AddDate(1, 0, 0)
		if err := a.Ensure(context.Background(), expired); err != nil {
			t.Fatalf("pending=%v: %v", pending, err)
		}
		state := decodeTestState(t, storage.body)
		if state.Current != 2 || len(state.Certs) != 2 || state.Certs[0].RetiredAt == nil {
			t.Fatalf("pending=%v: unexpected recovery: %+v", pending, state)
		}
		if err := a.Sync(context.Background(), expired); err != nil {
			t.Fatalf("recovered state did not sync: %v", err)
		}
	}
}

// TestExpiredCurrentRejected verifies Sync and Issue refuse an expired issuer.
func TestExpiredCurrentRejected(t *testing.T) {
	storage := new(fakeStorage)
	a := testAuthority(t, storage)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.Ensure(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	expired := start.AddDate(1, 0, 0)
	if err := a.Sync(context.Background(), expired); err == nil {
		t.Fatal("Sync accepted expired current CA")
	}
	if _, err := a.Issue("apps", "pod", "", expired); err == nil {
		t.Fatal("Issue accepted expired installed CA")
	}
}

// TestDecodeKey verifies strict base64 key decoding and whitespace handling.
func TestDecodeKey(t *testing.T) {
	want := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	encoded := append([]byte(" \n"), []byte(base64.StdEncoding.EncodeToString(want))...)
	encoded = append(encoded, '\n')
	got, err := DecodeKey(encoded)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("DecodeKey() = %x, %v", got, err)
	}
	for _, invalid := range [][]byte{[]byte("not base64"), []byte(base64.StdEncoding.EncodeToString(want[:31]))} {
		if _, err := DecodeKey(invalid); err == nil {
			t.Fatalf("DecodeKey accepted %q", invalid)
		}
	}
}

// TestSyncRejectsMalformedAndTamperedState verifies strict schema and CA-key authentication.
func TestSyncRejectsMalformedAndTamperedState(t *testing.T) {
	storage := new(fakeStorage)
	a := testAuthority(t, storage)
	now := time.Now().UTC()
	if err := a.Ensure(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	valid := append([]byte(nil), storage.body...)
	storage.body = append(valid[:len(valid)-1], []byte(`,"extra":true}`)...)
	if err := a.Sync(context.Background(), now); err == nil {
		t.Fatal("unknown field accepted")
	}
	state := decodeTestState(t, valid)
	state.Certs[0].DER[len(state.Certs[0].DER)-1] ^= 1
	storage.body, _ = json.Marshal(state)
	if err := a.Sync(context.Background(), now); err == nil {
		t.Fatal("tampered certificate accepted")
	}
}

// TestCrossAuthorityIssuance verifies shared CA-key issuance and the leaf profile.
func TestCrossAuthorityIssuance(t *testing.T) {
	storage := new(fakeStorage)
	first := testAuthority(t, storage)
	second := testAuthority(t, storage)
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := first.Ensure(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := first.Sync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := second.Sync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	material, err := second.Issue("apps", "database", "postgres", now)
	if err != nil {
		t.Fatal(err)
	}
	certificate := parseCertificate(t, material.Certificate)
	if certificate.PublicKeyAlgorithm != x509.Ed25519 || certificate.Subject.CommonName != "database" || len(certificate.URIs) != 1 || certificate.URIs[0].String() != "spiffe://demo.podmin.internal/ns/apps/pod/database" {
		t.Fatalf("unexpected identity: %+v", certificate)
	}
	wantNames := []string{"postgres.apps.svc.cluster.local", "postgres.apps.svc", "postgres.apps"}
	if !slices.Equal(certificate.DNSNames, wantNames) || !hasEKU(certificate, x509.ExtKeyUsageClientAuth) || !hasEKU(certificate, x509.ExtKeyUsageServerAuth) {
		t.Fatal("unexpected DNS or EKU profile")
	}
	for _, name := range wantNames {
		if err = certificate.VerifyHostname(name); err != nil {
			t.Fatalf("verify hostname %q: %v", name, err)
		}
	}
	if err = certificate.VerifyHostname("postgres"); err == nil {
		t.Fatal("bare Service name unexpectedly verified")
	}
	block, rest := pem.Decode(material.PrivateKey)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("key is not unencrypted PKCS#8 PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.(ed25519.PrivateKey); !ok {
		t.Fatalf("key type = %T", key)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material.CABundle) {
		t.Fatal("invalid bundle")
	}
	if _, err = certificate.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	other, err := first.Issue("apps", "other", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(material.CABundle, other.CABundle) || first.Revision() != 1 || second.Revision() != 1 {
		t.Fatal("authorities did not install identical durable state")
	}
}

// TestInputsCapAndRenewal verifies validation, CA expiry capping, and renewal boundary.
func TestInputsCapAndRenewal(t *testing.T) {
	storage := new(fakeStorage)
	a := testAuthority(t, storage)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := a.Issue("apps", "pod", "", now); err == nil {
		t.Fatal("issue before sync succeeded")
	}
	if err := a.Ensure(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Issue("Bad_Name", "pod", "", now); err == nil {
		t.Fatal("invalid name accepted")
	}
	material, err := a.Issue("apps", "pod", "", now.AddDate(1, 0, 0).Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !material.NotAfter.Equal(a.ca.NotAfter) {
		t.Fatal("leaf expiry was not capped")
	}
	if !NeedsRenewal(now.Add(6*time.Hour), now) || NeedsRenewal(now.Add(6*time.Hour+time.Nanosecond), now) {
		t.Fatal("unexpected renewal boundary")
	}
}

// testAuthority creates an authority with a stable test key.
func testAuthority(t *testing.T, storage s3lect.Storage) *Authority {
	t.Helper()
	a, err := New("demo", bytes.Repeat([]byte{7}, ed25519.SeedSize), storage)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// decodeTestState decodes durable state for test assertions.
func decodeTestState(t *testing.T, body []byte) caState {
	t.Helper()
	var state caState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// parseCertificate parses the first PEM certificate or fails the test.
func parseCertificate(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

// hasEKU reports whether certificate contains usage.
func hasEKU(certificate *x509.Certificate, usage x509.ExtKeyUsage) bool {
	for _, candidate := range certificate.ExtKeyUsage {
		if candidate == usage {
			return true
		}
	}
	return false
}

// countPEMCertificates counts consecutive certificates in a PEM bundle.
func countPEMCertificates(bundle []byte) int {
	count := 0
	for len(bundle) > 0 {
		block, rest := pem.Decode(bundle)
		if block == nil || block.Type != "CERTIFICATE" {
			break
		}
		count++
		bundle = rest
	}
	return count
}

// TestNewValidation verifies constructor requirements.
func TestNewValidation(t *testing.T) {
	if _, err := New("bad_cluster", make([]byte, 32), new(fakeStorage)); err == nil {
		t.Fatal("bad cluster accepted")
	}
	if _, err := New("demo", make([]byte, 31), new(fakeStorage)); err == nil {
		t.Fatal("bad key accepted")
	}
	if _, err := New("demo", make([]byte, 32), nil); err == nil {
		t.Fatal("missing storage accepted")
	}
}
