// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package workload

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"sync"
	"time"

	"github.com/podmin-dev/podmin/internal/manifest"
	"github.com/podplane/s3lect"
)

const (
	// VolumeName is the name of the Podmin workload identity volume.
	VolumeName = "podmin-identity"
	// MountPath is the workload identity volume mount path.
	MountPath = manifest.IdentityMountPath
	// CertificateFilename is the leaf certificate filename.
	CertificateFilename = "tls.crt"
	// PrivateKeyFilename is the leaf private key filename.
	PrivateKeyFilename = "tls.key"
	// CABundleFilename is the CA trust bundle filename.
	CABundleFilename = "ca.crt"
	// CAStateKey is the durable workload CA state object key.
	CAStateKey = "identity/ca.json"

	stateVersion  = 1
	maxStateSize  = 32 << 10
	maxCASRetries = 8
	clockSkew     = 5 * time.Minute
	activationLag = 10 * time.Minute
	retention     = 30 * 24 * time.Hour
)

// Material contains a newly issued workload identity and its trust bundle.
type Material struct {
	Certificate []byte
	PrivateKey  []byte
	CABundle    []byte
	NotAfter    time.Time
	SPIFFEID    string
}

// certificateRecord is one public CA generation in durable state.
type certificateRecord struct {
	Generation uint64     `json:"generation"`
	DER        []byte     `json:"der"`
	ActivateAt *time.Time `json:"activateAt,omitempty"`
	RetiredAt  *time.Time `json:"retiredAt,omitempty"`
}

// caState is the bounded public durable CA document.
type caState struct {
	Version int                 `json:"version"`
	Current uint64              `json:"current"`
	Certs   []certificateRecord `json:"certs"`
}

// Authority issues workload certificates from the stable workload CA key.
type Authority struct {
	mu       sync.RWMutex
	cluster  string
	key      ed25519.PrivateKey
	storage  s3lect.Storage
	ca       *x509.Certificate
	bundle   []byte
	state    []byte
	revision uint64
}

// New validates configuration and creates a cluster identity authority.
func New(cluster string, key []byte, storage s3lect.Storage) (*Authority, error) {
	if !manifest.ValidID(cluster) || len(key) != ed25519.SeedSize || storage == nil {
		return nil, errors.New("valid cluster, 32-byte workload CA key, and storage are required")
	}
	return &Authority{cluster: cluster, key: ed25519.NewKeyFromSeed(key), storage: storage}, nil
}

// DecodeKey decodes the base64 workload CA key stored by infrastructure.
func DecodeKey(encoded []byte) ([]byte, error) {
	key := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, err := base64.StdEncoding.Decode(key, bytes.TrimSpace(encoded))
	if err != nil || n != ed25519.SeedSize {
		return nil, errors.New("workload CA key must be base64-encoded 32 bytes")
	}
	return key[:n], nil
}

// Revision returns a local revision which changes whenever a new durable state is installed.
func (a *Authority) Revision() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.revision
}

// Ensure creates or rotates the durable CA state using bounded compare-and-swap retries.
func (a *Authority) Ensure(ctx context.Context, now time.Time) error {
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		body, etag, err := a.storage.Get(ctx, CAStateKey)
		if errors.Is(err, s3lect.ErrStorageNotFound) {
			record, createErr := a.newRecord(1, now)
			if createErr != nil {
				return createErr
			}
			state := caState{Version: stateVersion, Current: 1, Certs: []certificateRecord{record}}
			encoded, encodeErr := encodeState(state)
			if encodeErr != nil {
				return encodeErr
			}
			err = a.storage.PutIfMatch(ctx, CAStateKey, encoded, "")
			if errors.Is(err, s3lect.ErrStoragePrecondition) {
				continue
			}
			return err
		}
		if err != nil {
			return fmt.Errorf("load CA state: %w", err)
		}
		state, _, err := a.validateState(body, now, false)
		if err != nil {
			return err
		}
		changed := pruneState(&state, now)
		currentPosition := currentIndex(state)
		current := state.Certs[currentPosition]
		certificate, _ := x509.ParseCertificate(current.DER)
		pendingPosition := pendingIndex(state)
		if !now.Before(certificate.NotAfter) {
			var pendingCertificate *x509.Certificate
			if pendingPosition >= 0 {
				pendingCertificate, _ = x509.ParseCertificate(state.Certs[pendingPosition].DER)
			}
			if pendingPosition < 0 || now.Before(pendingCertificate.NotBefore) || !now.Before(pendingCertificate.NotAfter) {
				next, createErr := a.newRecord(current.Generation+1, now)
				if createErr != nil {
					return createErr
				}
				state.Certs = []certificateRecord{current, next}
				pendingPosition = 1
			}
			promoteState(&state, pendingPosition, now)
			changed = true
		} else if pendingPosition >= 0 && !now.Before(*state.Certs[pendingPosition].ActivateAt) {
			promoteState(&state, pendingPosition, now)
			changed = true
		} else if pendingPosition < 0 && !certificate.NotAfter.After(now.Add(retention)) {
			next, createErr := a.newRecord(current.Generation+1, now)
			if createErr != nil {
				return createErr
			}
			pendingCertificate, parseErr := x509.ParseCertificate(next.DER)
			if parseErr != nil {
				return parseErr
			}
			activate := pendingCertificate.NotBefore.Add(clockSkew + activationLag).UTC()
			next.ActivateAt = &activate
			state.Certs = append(state.Certs, next)
			changed = true
		}
		if !changed {
			return nil
		}
		encoded, encodeErr := encodeState(state)
		if encodeErr != nil {
			return encodeErr
		}
		err = a.storage.PutIfMatch(ctx, CAStateKey, encoded, etag)
		if errors.Is(err, s3lect.ErrStoragePrecondition) {
			continue
		}
		return err
	}
	return errors.New("CA state changed too frequently")
}

// Sync validates and installs the current durable CA state.
func (a *Authority) Sync(ctx context.Context, now time.Time) error {
	body, _, err := a.storage.Get(ctx, CAStateKey)
	if err != nil {
		return fmt.Errorf("load CA state: %w", err)
	}
	state, certificates, err := a.validateState(body, now, true)
	if err != nil {
		return err
	}
	index := currentIndex(state)
	bundle := make([]byte, 0)
	for _, record := range state.Certs {
		bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: record.DER})...)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !bytes.Equal(a.state, body) {
		a.revision++
		a.state = append(a.state[:0], body...)
	}
	a.ca = certificates[index]
	a.bundle = bundle
	return nil
}

// Issue creates a short-lived workload identity for a Pod and optional Service.
func (a *Authority) Issue(namespace, pod, service string, now time.Time) (Material, error) {
	if !manifest.ValidNamespace(namespace) || !manifest.ValidID(pod) || (service != "" && !manifest.ValidID(service)) {
		return Material{}, errors.New("namespace, Pod, or Service name is invalid")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Material{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return Material{}, err
	}
	spiffeID := fmt.Sprintf("spiffe://%s.podmin.internal/ns/%s/pod/%s", a.cluster, namespace, pod)
	uri, err := url.Parse(spiffeID)
	if err != nil {
		return Material{}, fmt.Errorf("construct SPIFFE ID: %w", err)
	}
	a.mu.RLock()
	if a.ca == nil {
		a.mu.RUnlock()
		return Material{}, errors.New("CA state has not been synchronized")
	}
	if now.Before(a.ca.NotBefore) || !now.Before(a.ca.NotAfter) {
		a.mu.RUnlock()
		return Material{}, errors.New("current CA is not currently valid")
	}
	notAfter := now.Add(24 * time.Hour)
	if a.ca.NotAfter.Before(notAfter) {
		notAfter = a.ca.NotAfter
	}
	if !notAfter.After(now) {
		a.mu.RUnlock()
		return Material{}, errors.New("leaf certificate validity is nonpositive")
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: pod}, NotBefore: now.Add(-5 * time.Minute), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true, URIs: []*url.URL{uri}}
	if service != "" {
		template.ExtKeyUsage = append(template.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		serviceNamespace := service + "." + namespace
		template.DNSNames = []string{serviceNamespace + ".svc.cluster.local", serviceNamespace + ".svc", serviceNamespace}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.ca, publicKey, a.key)
	bundle := append([]byte(nil), a.bundle...)
	a.mu.RUnlock()
	if err != nil {
		return Material{}, fmt.Errorf("create leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return Material{}, fmt.Errorf("marshal leaf key: %w", err)
	}
	return Material{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), CABundle: bundle, NotAfter: notAfter, SPIFFEID: spiffeID}, nil
}

// NeedsRenewal reports whether an identity expires within the six-hour renewal window.
func NeedsRenewal(notAfter, now time.Time) bool { return !notAfter.After(now.Add(6 * time.Hour)) }

// newRecord creates a self-signed CA certificate record.
func (a *Authority) newRecord(generation uint64, now time.Time) (certificateRecord, error) {
	serial := new(big.Int).SetUint64(generation)
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: fmt.Sprintf("%s workload CA %d", a.cluster, generation)}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, a.key.Public(), a.key)
	if err != nil {
		return certificateRecord{}, fmt.Errorf("create CA certificate: %w", err)
	}
	return certificateRecord{Generation: generation, DER: der}, nil
}

// validateState strictly decodes and authenticates a durable state.
func (a *Authority) validateState(body []byte, now time.Time, requireCurrentValid bool) (caState, []*x509.Certificate, error) {
	if len(body) == 0 || len(body) > maxStateSize {
		return caState{}, nil, errors.New("CA state has invalid size")
	}
	var state caState
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, nil, fmt.Errorf("decode CA state: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return state, nil, err
	}
	if state.Version != stateVersion || state.Current == 0 || len(state.Certs) == 0 || len(state.Certs) > 2 {
		return state, nil, errors.New("CA state schema is invalid")
	}
	seen := map[uint64]bool{}
	certificates := make([]*x509.Certificate, len(state.Certs))
	currentFound := false
	for index, record := range state.Certs {
		if record.Generation == 0 || seen[record.Generation] || (index > 0 && state.Certs[index-1].Generation >= record.Generation) || len(record.DER) == 0 || len(record.DER) > 8<<10 {
			return state, nil, errors.New("CA certificate record is invalid")
		}
		seen[record.Generation] = true
		certificate, err := x509.ParseCertificate(record.DER)
		public, ok := certificatePublicKey(certificate)
		if err != nil || !ok || !bytes.Equal(public, a.key.Public().(ed25519.PublicKey)) || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || !certificate.NotAfter.After(certificate.NotBefore) || certificate.CheckSignatureFrom(certificate) != nil {
			return state, nil, errors.New("CA certificate is invalid or does not match its generation")
		}
		if record.ActivateAt != nil && record.RetiredAt != nil {
			return state, nil, errors.New("CA certificate lifecycle is invalid")
		}
		if record.Generation == state.Current {
			if record.RetiredAt != nil || record.ActivateAt != nil || now.Add(clockSkew).Before(certificate.NotBefore) || (requireCurrentValid && (now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter))) {
				return state, nil, errors.New("current CA is not usable for issuing")
			}
			currentFound = true
		} else if record.ActivateAt != nil {
			expectedActivation := certificate.NotBefore.Add(clockSkew + activationLag)
			if record.Generation < state.Current || !record.ActivateAt.Equal(expectedActivation) || record.ActivateAt.After(certificate.NotAfter) {
				return state, nil, errors.New("pending CA activation is invalid")
			}
		} else if record.RetiredAt == nil || record.Generation > state.Current || record.RetiredAt.After(now.Add(clockSkew)) {
			return state, nil, errors.New("previous CA retirement is invalid")
		}
		certificates[index] = certificate
	}
	if !currentFound {
		return state, nil, errors.New("current CA generation is missing")
	}
	return state, certificates, nil
}

// pendingIndex returns the index of the pending generation, or -1.
func pendingIndex(state caState) int {
	for index := range state.Certs {
		if state.Certs[index].ActivateAt != nil {
			return index
		}
	}
	return -1
}

// promoteState makes a pending generation current and retires the prior issuer.
func promoteState(state *caState, pending int, now time.Time) {
	retired := now.UTC()
	state.Certs[currentIndex(*state)].RetiredAt = &retired
	state.Certs[pending].ActivateAt = nil
	state.Current = state.Certs[pending].Generation
}

// certificatePublicKey safely obtains an Ed25519 certificate key.
func certificatePublicKey(certificate *x509.Certificate) (ed25519.PublicKey, bool) {
	if certificate == nil || certificate.PublicKeyAlgorithm != x509.Ed25519 {
		return nil, false
	}
	key, ok := certificate.PublicKey.(ed25519.PublicKey)
	return key, ok
}

// ensureJSONEnd rejects trailing JSON values.
func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("CA state contains trailing data")
	}
	return nil
}

// currentIndex returns the index of the current generation.
func currentIndex(state caState) int {
	for index := range state.Certs {
		if state.Certs[index].Generation == state.Current {
			return index
		}
	}
	return -1
}

// pruneState removes a previous certificate after its overlap window.
func pruneState(state *caState, now time.Time) bool {
	changed := false
	kept := state.Certs[:0]
	for _, record := range state.Certs {
		if record.Generation != state.Current && record.RetiredAt != nil && !record.RetiredAt.Add(retention+clockSkew).After(now) {
			changed = true
			continue
		}
		kept = append(kept, record)
	}
	state.Certs = kept
	return changed
}

// encodeState produces the canonical bounded durable representation.
func encodeState(state caState) ([]byte, error) {
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > maxStateSize {
		return nil, errors.New("encode CA state")
	}
	return encoded, nil
}

// randomSerial returns a random positive 128-bit certificate serial number.
func randomSerial() (*big.Int, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	b[0] &= 0x7f
	if new(big.Int).SetBytes(b).Sign() == 0 {
		b[15] = 1
	}
	return new(big.Int).SetBytes(b), nil
}
