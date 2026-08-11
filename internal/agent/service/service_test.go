// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"net"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

// dnsRecorder captures an in-process DNS response.
type dnsRecorder struct{ message *dns.Msg }

// LocalAddr returns a dummy local address.
func (d *dnsRecorder) LocalAddr() net.Addr { return &net.UDPAddr{} }

// RemoteAddr returns a dummy remote address.
func (d *dnsRecorder) RemoteAddr() net.Addr { return &net.UDPAddr{} }

// WriteMsg captures a DNS message.
func (d *dnsRecorder) WriteMsg(message *dns.Msg) error { d.message = message; return nil }

// Write is unused by the handler.
func (d *dnsRecorder) Write([]byte) (int, error) { return 0, nil }

// Close closes the dummy writer.
func (d *dnsRecorder) Close() error { return nil }

// TsigStatus reports no TSIG error.
func (d *dnsRecorder) TsigStatus() error { return nil }

// TsigTimersOnly ignores TSIG timer selection.
func (d *dnsRecorder) TsigTimersOnly(bool) {}

// Hijack is unsupported by the dummy writer.
func (d *dnsRecorder) Hijack() {}

// TestDNSExpandsShortNames verifies authoritative short-name AAAA answers.
func TestDNSExpandsShortNames(t *testing.T) {
	t.Parallel()
	handler := NewServer()
	handler.Replace(map[string][]net.IP{"api.default.svc.cluster.local": {net.ParseIP("2001:db8::1")}})
	request := new(dns.Msg)
	request.SetQuestion("api.", dns.TypeAAAA)
	recorder := &dnsRecorder{}
	handler.ServeDNS(recorder, request)
	if recorder.message == nil || !recorder.message.Authoritative || len(recorder.message.Answer) != 1 {
		t.Fatalf("unexpected response: %#v", recorder.message)
	}
}

// TestServerReplacementOwnsInput verifies caller mutations cannot race with or alter DNS answers.
func TestServerReplacementOwnsInput(t *testing.T) {
	handler := NewServer()
	address := net.ParseIP("2001:db8::1")
	input := map[string][]net.IP{"api.default.svc.cluster.local": {address}}
	handler.Replace(input)
	input["api.default.svc.cluster.local"][0][0] = 0
	delete(input, "api.default.svc.cluster.local")
	request := new(dns.Msg)
	request.SetQuestion("api.", dns.TypeAAAA)
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			recorder := &dnsRecorder{}
			handler.ServeDNS(recorder, request)
			if recorder.message == nil || len(recorder.message.Answer) != 1 {
				t.Errorf("unexpected isolated response: %#v", recorder.message)
			}
		}()
	}
	group.Wait()
}
