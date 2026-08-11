// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

const (
	// ClusterCAPathSuffix is the reserved cluster CA parameter suffix.
	ClusterCAPathSuffix = "/_system/cluster-ca"
	leafLifetime        = 24 * time.Hour
)

// secret is the versioned cluster CA SecureString representation.
type secret struct {
	Version     int    `json:"version"`
	Cluster     string `json:"cluster"`
	Certificate string `json:"certificate_pem"`
	PrivateKey  string `json:"private_key_pem"`
}

// Authority is a validated cluster certificate authority.
type Authority struct {
	certificate *x509.Certificate
	privateKey  ed25519.PrivateKey
}

// NodeIdentity is an authenticated Podmin node identity.
type NodeIdentity struct {
	NodeID    string
	NodeGroup string
}

// Generate creates a long-lived Ed25519 cluster CA as a versioned JSON secret.
func Generate(cluster string) ([]byte, error) {
	if err := validatePart(cluster); err != nil {
		return nil, fmt.Errorf("cluster: %w", err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{Organization: []string{"Podmin"}, OrganizationalUnit: []string{cluster + " cluster CA"}}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(20, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	return json.Marshal(secret{Version: 1, Cluster: cluster, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))})
}

// Load parses and validates a cluster CA secret, including its key match and CA constraints.
func Load(value []byte, cluster string) (*Authority, error) {
	var encoded secret
	if err := json.Unmarshal(value, &encoded); err != nil || encoded.Version != 1 || encoded.Cluster != cluster || validatePart(cluster) != nil {
		return nil, errors.New("invalid cluster CA secret version or JSON")
	}
	certificateBlock, rest := pem.Decode([]byte(encoded.Certificate))
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid cluster CA certificate PEM")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	now := time.Now()
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.MaxPathLen != 0 || !certificate.MaxPathLenZero || certificate.KeyUsage&x509.KeyUsageCertSign == 0 || certificate.PublicKeyAlgorithm != x509.Ed25519 || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) || len(certificate.Subject.OrganizationalUnit) != 1 || certificate.Subject.OrganizationalUnit[0] != cluster+" cluster CA" || certificate.CheckSignatureFrom(certificate) != nil {
		return nil, errors.New("invalid cluster CA certificate constraints")
	}
	keyBlock, rest := pem.Decode([]byte(encoded.PrivateKey))
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("invalid cluster CA private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok || !certificate.PublicKey.(ed25519.PublicKey).Equal(privateKey.Public()) {
		return nil, errors.New("cluster CA key does not match certificate")
	}
	return &Authority{certificate: certificate, privateKey: privateKey}, nil
}

// TLSConfigs returns distinct TLS 1.3 client and server configurations with an automatically renewed in-memory node certificate.
func (a *Authority) TLSConfigs(cluster, nodeID, nodeGroup string, address netip.Addr) (*tls.Config, *tls.Config, error) {
	if err := validatePart(cluster); err != nil {
		return nil, nil, err
	}
	if err := validatePart(nodeID); err != nil {
		return nil, nil, err
	}
	if err := validatePart(nodeGroup); err != nil {
		return nil, nil, err
	}
	if !address.Is6() || address.Is4In6() || !address.IsGlobalUnicast() {
		return nil, nil, errors.New("node address must be global-unicast IPv6")
	}
	provider := &certificateProvider{authority: a, cluster: cluster, nodeID: nodeID, nodeGroup: nodeGroup, address: address}
	if _, err := provider.current(); err != nil {
		return nil, nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(a.certificate)
	client := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return provider.current()
	}}
	server := &tls.Config{MinVersion: tls.VersionTLS13, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return provider.current()
	}}
	return client, server, nil
}

// IdentityFromContext extracts a node identity from a verified gRPC TLS peer and exact cluster trust domain.
func IdentityFromContext(ctx context.Context, cluster string) (NodeIdentity, error) {
	value, ok := peer.FromContext(ctx)
	info, tlsOK := value.AuthInfo.(credentials.TLSInfo)
	if !ok || !tlsOK || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return NodeIdentity{}, errors.New("coordination peer is not verified with TLS")
	}
	leaf := info.State.VerifiedChains[0][0]
	if len(leaf.URIs) != 1 {
		return NodeIdentity{}, errors.New("node certificate must contain exactly one URI SAN")
	}
	uri := leaf.URIs[0]
	parts := strings.Split(strings.Trim(uri.EscapedPath(), "/"), "/")
	if uri.Scheme != "spiffe" || uri.Host != trustDomain(cluster) || len(parts) != 4 || parts[0] != "node" || parts[2] != "nodegroup" {
		return NodeIdentity{}, errors.New("invalid node SPIFFE identity")
	}
	nodeID, err := url.PathUnescape(parts[1])
	if err != nil {
		return NodeIdentity{}, errors.New("invalid node ID in SPIFFE identity")
	}
	nodeGroup, err := url.PathUnescape(parts[3])
	if err != nil || validatePart(nodeID) != nil || validatePart(nodeGroup) != nil {
		return NodeIdentity{}, errors.New("invalid node SPIFFE identity components")
	}
	return NodeIdentity{NodeID: nodeID, NodeGroup: nodeGroup}, nil
}

// certificateProvider owns one renewable in-memory node keypair.
type certificateProvider struct {
	sync.Mutex
	authority                  *Authority
	cluster, nodeID, nodeGroup string
	address                    netip.Addr
	certificate                *tls.Certificate
}

// current returns a certificate, replacing it once one quarter of its lifetime remains.
func (p *certificateProvider) current() (*tls.Certificate, error) {
	p.Lock()
	defer p.Unlock()
	now := time.Now()
	if p.certificate != nil && len(p.certificate.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(p.certificate.Certificate[0])
		if err == nil && now.Before(leaf.NotAfter.Add(-leafLifetime/4)) {
			return p.certificate, nil
		}
	}
	certificate, err := p.authority.issue(p.cluster, p.nodeID, p.nodeGroup, p.address, now)
	if err != nil {
		return nil, err
	}
	p.certificate = certificate
	return certificate, nil
}

// issue creates one short-lived Ed25519 client/server node certificate.
func (a *Authority) issue(cluster, nodeID, nodeGroup string, address netip.Addr, now time.Time) (*tls.Certificate, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	identity := &url.URL{Scheme: "spiffe", Host: trustDomain(cluster), Path: "/node/" + nodeID + "/nodegroup/" + nodeGroup}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(leafLifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.IP(address.AsSlice())}, URIs: []*url.URL{identity}}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, public, a.privateKey)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.certificate.Raw}, PrivateKey: private}, nil
}

// serialNumber creates a positive random 128-bit certificate serial number.
func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

// trustDomain returns the exact cluster-specific SPIFFE trust domain.
func trustDomain(cluster string) string { return cluster + ".podmin.internal" }

// validatePart rejects empty or path-ambiguous identity values.
func validatePart(value string) error {
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return errors.New("identity component is empty or contains reserved characters")
	}
	return nil
}
