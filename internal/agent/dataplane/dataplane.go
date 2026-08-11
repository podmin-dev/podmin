// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dataplane

import (
	"context"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"sync"
)

// Protocol is an IP transport protocol number.
type Protocol uint8

const (
	// maxTableEntries matches the capacity of the service and backend BPF maps.
	maxTableEntries = 65536
	// ProtocolTCP is the TCP transport protocol.
	ProtocolTCP Protocol = 6
	// ProtocolUDP is the UDP transport protocol.
	ProtocolUDP Protocol = 17
)

var (
	// ErrUnsupported is returned when dataplane activation is unavailable.
	ErrUnsupported = errors.New("IPv6 service eBPF dataplane activation is unsupported")
	// ErrInvalidSnapshot is returned for an invalid complete snapshot.
	ErrInvalidSnapshot = errors.New("invalid dataplane snapshot")
)

// Backend is a ready service endpoint.
type Backend struct {
	Address netip.Addr
	Port    uint16
}

// Port describes one service transport port and its ready endpoints.
type Port struct {
	Protocol Protocol
	Port     uint16
	Backends []Backend
}

// Service describes a stable IPv6 virtual service.
type Service struct {
	VIP   netip.Addr
	Ports []Port
}

// Snapshot is the controller's complete desired dataplane state.
type Snapshot struct {
	NodeIPv6 netip.Addr
	Services []Service
}

// ServiceKey is the exact key installed in the service lookup map.
type ServiceKey struct {
	VIP      [16]byte
	Port     uint16
	Protocol Protocol
}

// ServiceValue identifies a contiguous range in the backend map.
type ServiceValue struct {
	BackendOffset uint32
	BackendCount  uint32
}

// BackendValue is the map representation of a ready endpoint.
type BackendValue struct {
	Address [16]byte
	Port    uint16
}

// Tables are immutable userspace map contents ready for kernel publication.
type Tables struct {
	NodeIPv6 [16]byte
	Services map[ServiceKey]ServiceValue
	Backends []BackendValue
}

// Reconciler owns one unpinned dataplane lifecycle.
type Reconciler struct {
	reconcileMu sync.Mutex
	mu          sync.RWMutex
	podPrefix   netip.Prefix
	refreshHint chan struct{}
	tables      Tables
	state       *platformState
}

// New creates an inactive dataplane reconciler.
func New(podPrefix netip.Prefix) *Reconciler {
	return &Reconciler{podPrefix: podPrefix, refreshHint: make(chan struct{}, 1)}
}

// TriggerRefresh requests asynchronous interface discovery when the dataplane is active.
func (r *Reconciler) TriggerRefresh() {
	select {
	case r.refreshHint <- struct{}{}:
	default:
	}
}

// DeriveVIP deterministically derives a VIP in fd50:6f64:6d69::/64.
func DeriveVIP(cluster, namespace, name string) netip.Addr {
	sum := sha512.Sum512([]byte(cluster + "\x00" + namespace + "\x00" + name))
	var address [16]byte
	copy(address[:8], []byte{0xfd, 0x50, 0x6f, 0x64, 0x6d, 0x69, 0, 0})
	copy(address[8:], sum[:8])
	return netip.AddrFrom16(address)
}

// Compile validates and converts a complete snapshot into deterministic maps.
func Compile(snapshot Snapshot) (Tables, error) {
	tables := Tables{Services: make(map[ServiceKey]ServiceValue)}
	if len(snapshot.Services) == 0 {
		return tables, nil
	}
	if !snapshot.NodeIPv6.Is6() || snapshot.NodeIPv6.Is4In6() {
		return Tables{}, fmt.Errorf("%w: node address must be IPv6", ErrInvalidSnapshot)
	}
	tables.NodeIPv6 = snapshot.NodeIPv6.As16()
	type entry struct {
		key      ServiceKey
		backends []BackendValue
	}
	var entries []entry
	seenKeys := make(map[ServiceKey]struct{})
	backendCount := 0
	for _, service := range snapshot.Services {
		if !service.VIP.Is6() || service.VIP.Is4In6() {
			return Tables{}, fmt.Errorf("%w: service VIP must be IPv6", ErrInvalidSnapshot)
		}
		for _, port := range service.Ports {
			if port.Port == 0 || (port.Protocol != ProtocolTCP && port.Protocol != ProtocolUDP) {
				return Tables{}, fmt.Errorf("%w: service protocol or port", ErrInvalidSnapshot)
			}
			item := entry{key: ServiceKey{VIP: service.VIP.As16(), Port: port.Port, Protocol: port.Protocol}}
			if _, exists := seenKeys[item.key]; exists {
				return Tables{}, fmt.Errorf("%w: duplicate service port", ErrInvalidSnapshot)
			}
			if len(entries) == maxTableEntries {
				return Tables{}, fmt.Errorf("%w: too many service ports", ErrInvalidSnapshot)
			}
			seenKeys[item.key] = struct{}{}
			if len(port.Backends) > maxTableEntries-backendCount {
				return Tables{}, fmt.Errorf("%w: too many backends", ErrInvalidSnapshot)
			}
			backendCount += len(port.Backends)
			for _, backend := range port.Backends {
				if !backend.Address.Is6() || backend.Address.Is4In6() || backend.Port == 0 {
					return Tables{}, fmt.Errorf("%w: backend address or port", ErrInvalidSnapshot)
				}
				item.backends = append(item.backends, BackendValue{Address: backend.Address.As16(), Port: backend.Port})
			}
			sort.Slice(item.backends, func(i, j int) bool {
				if item.backends[i].Address != item.backends[j].Address {
					return string(item.backends[i].Address[:]) < string(item.backends[j].Address[:])
				}
				return item.backends[i].Port < item.backends[j].Port
			})
			entries = append(entries, item)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return keyLess(entries[i].key, entries[j].key) })
	for _, item := range entries {
		offset := len(tables.Backends)
		tables.Backends = append(tables.Backends, item.backends...)
		tables.Services[item.key] = ServiceValue{BackendOffset: uint32(offset), BackendCount: uint32(len(item.backends))}
	}
	return tables, nil
}

// SelectBackend deterministically selects a backend for opaque flow bytes.
func (t Tables) SelectBackend(key ServiceKey, flow []byte) (BackendValue, bool) {
	service, ok := t.Services[key]
	if !ok || service.BackendCount == 0 {
		return BackendValue{}, false
	}
	sum := sha512.Sum512(flow)
	index := service.BackendOffset + uint32(binary.BigEndian.Uint64(sum[:8])%uint64(service.BackendCount))
	if int(index) >= len(t.Backends) {
		return BackendValue{}, false
	}
	return t.Backends[index], true
}

// Reconcile validates and takes ownership of a complete snapshot before platform activation.
func (r *Reconciler) Reconcile(ctx context.Context, snapshot Snapshot) error {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	tables, err := Compile(snapshot)
	if err != nil {
		return err
	}
	r.mu.RLock()
	old, unchanged := r.state, reflect.DeepEqual(r.tables, tables)
	r.mu.RUnlock()
	if unchanged && (old != nil || len(snapshot.Services) == 0) {
		if old != nil {
			return old.refresh()
		}
		return nil
	}
	if len(snapshot.Services) != 0 {
		if !r.podPrefix.IsValid() || !r.podPrefix.Addr().Is6() || r.podPrefix.Addr().Is4In6() || r.podPrefix != r.podPrefix.Masked() {
			return fmt.Errorf("%w: Pod prefix must be an exact IPv6 prefix", ErrInvalidSnapshot)
		}
		state, err := activate(ctx, tables, r.podPrefix, r.refreshHint, old)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.state = state
		r.tables = cloneTables(tables)
		r.mu.Unlock()
		if old != nil {
			_ = old.close()
		}
		return nil
	}
	if old != nil {
		if err := old.close(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.state = nil
	r.tables = cloneTables(tables)
	r.mu.Unlock()
	return nil
}

// Tables returns an ownership-safe copy of the currently active map model.
func (r *Reconciler) Tables() Tables {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneTables(r.tables)
}

// Close releases the unpinned dataplane state.
func (r *Reconciler) Close() error {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if state != nil {
		if err := state.close(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.state = nil
	r.tables = Tables{}
	r.mu.Unlock()
	return nil
}

// keyLess orders service map keys reproducibly.
func keyLess(left, right ServiceKey) bool {
	if left.VIP != right.VIP {
		return string(left.VIP[:]) < string(right.VIP[:])
	}
	if left.Protocol != right.Protocol {
		return left.Protocol < right.Protocol
	}
	return left.Port < right.Port
}

// cloneTables makes all mutable table storage independent.
func cloneTables(input Tables) Tables {
	output := Tables{NodeIPv6: input.NodeIPv6, Services: make(map[ServiceKey]ServiceValue, len(input.Services)), Backends: append([]BackendValue(nil), input.Backends...)}
	for key, value := range input.Services {
		output.Services[key] = value
	}
	return output
}
