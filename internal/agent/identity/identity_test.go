// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/netip"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// TestGenerateLoadAndIssue verifies CA validation and required node SANs and EKUs.
func TestGenerateLoadAndIssue(t *testing.T) {
	encoded, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := Load(encoded, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := authority.TLSConfigs("cluster", "node", "workers", netip.MustParseAddr("2001:db8::1"))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := server.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if client.MinVersion != 0x0304 || server.MinVersion != 0x0304 || len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "2001:db8::1" || len(leaf.URIs) != 1 || len(leaf.ExtKeyUsage) != 2 || len(leaf.Subject.CommonName) != 0 {
		t.Fatalf("unexpected TLS configuration or leaf: %#v", leaf)
	}
}

// TestLoadRejectsMismatchedKey verifies a CA certificate cannot be paired with another key.
func TestLoadRejectsMismatchedKey(t *testing.T) {
	first, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	var one, two secret
	if json.Unmarshal(first, &one) != nil || json.Unmarshal(second, &two) != nil {
		t.Fatal("decode generated secrets")
	}
	one.PrivateKey = two.PrivateKey
	mismatch, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Load(mismatch, "cluster"); err == nil {
		t.Fatal("mismatched key was accepted")
	}
}

// TestLoadRejectsAnotherCluster verifies cluster CA secrets are bound to one trust domain.
func TestLoadRejectsAnotherCluster(t *testing.T) {
	encoded, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Load(encoded, "other"); err == nil {
		t.Fatal("cluster CA was accepted for another cluster")
	}
}

// TestCertificateProviderRenewsExpiredLeaf verifies callbacks replace stale in-memory node certificates.
func TestCertificateProviderRenewsExpiredLeaf(t *testing.T) {
	encoded, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := Load(encoded, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	provider := &certificateProvider{authority: authority, cluster: "cluster", nodeID: "node", nodeGroup: "workers", address: netip.MustParseAddr("2001:db8::1")}
	provider.certificate, err = authority.issue(provider.cluster, provider.nodeID, provider.nodeGroup, provider.address, time.Now().Add(-2*leafLifetime))
	if err != nil {
		t.Fatal(err)
	}
	stale := provider.certificate.Certificate[0]
	current, err := provider.current()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stale, current.Certificate[0]) {
		t.Fatal("expired node certificate was not renewed")
	}
}

// TestTLSConfigsAuthenticateNodeIdentity verifies mutual TLS and SPIFFE identity extraction together.
func TestTLSConfigsAuthenticateNodeIdentity(t *testing.T) {
	encoded, err := Generate("cluster")
	if err != nil {
		t.Fatal(err)
	}
	authority, err := Load(encoded, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	clientConfig, _, err := authority.TLSConfigs("cluster", "client", "workers", netip.MustParseAddr("2001:db8::2"))
	if err != nil {
		t.Fatal(err)
	}
	_, serverConfig, err := authority.TLSConfigs("cluster", "server", "workers", netip.MustParseAddr("2001:db8::1"))
	if err != nil {
		t.Fatal(err)
	}
	clientConfig.ServerName = "2001:db8::1"
	serverSide, clientSide := net.Pipe()
	server := tls.Server(serverSide, serverConfig)
	client := tls.Client(clientSide, clientConfig)
	t.Cleanup(func() { _ = serverSide.Close(); _ = clientSide.Close() })
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.HandshakeContext(context.Background()) }()
	if err = client.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = <-serverResult; err != nil {
		t.Fatal(err)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: server.ConnectionState()}})
	identity, err := IdentityFromContext(ctx, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeID != "client" || identity.NodeGroup != "workers" {
		t.Fatalf("unexpected authenticated identity: %#v", identity)
	}
}
