// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"net"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

// Server serves authoritative deployment IPv6 records from a replaceable snapshot.
type Server struct {
	mu        sync.RWMutex
	endpoints map[string][]net.IP
}

// NewServer constructs an empty authoritative Kubernetes Service DNS handler.
func NewServer() *Server { return &Server{endpoints: map[string][]net.IP{}} }

// Replace atomically stores an owned copy of all known full deployment names.
func (d *Server) Replace(endpoints map[string][]net.IP) {
	owned := make(map[string][]net.IP, len(endpoints))
	for name, addresses := range endpoints {
		owned[name] = make([]net.IP, len(addresses))
		for index, address := range addresses {
			owned[name][index] = append(net.IP(nil), address...)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.endpoints = owned
}

// ServeDNS answers AAAA queries and expands Service and Service.namespace short forms.
func (d *Server) ServeDNS(writer dns.ResponseWriter, request *dns.Msg) {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Authoritative = true
	if len(request.Question) == 0 {
		_ = writer.WriteMsg(response)
		return
	}
	question := request.Question[0]
	name := strings.TrimSuffix(strings.ToLower(question.Name), ".")
	labels := strings.Split(name, ".")
	if len(labels) == 1 {
		name = labels[0] + ".default.svc.cluster.local"
	} else if len(labels) == 2 {
		name = labels[0] + "." + labels[1] + ".svc.cluster.local"
	}
	d.mu.RLock()
	addresses, found := d.endpoints[name]
	d.mu.RUnlock()
	if !found {
		response.Rcode = dns.RcodeNameError
	} else if question.Qtype == dns.TypeAAAA || question.Qtype == dns.TypeANY {
		for _, address := range addresses {
			if ipv6 := address.To16(); ipv6 != nil && address.To4() == nil {
				response.Answer = append(response.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 5}, AAAA: ipv6})
			}
		}
	}
	_ = writer.WriteMsg(response)
}
