// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package dataplane

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

// platformState owns all unpinned kernel objects and TCX links.
type platformState struct {
	mu      sync.Mutex
	objects bpfObjects
	links   map[int]link.Link
	prefix  netip.Prefix
	hints   <-chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// activate loads a complete isolated service table while retaining live flow maps.
func activate(ctx context.Context, tables Tables, prefix netip.Prefix, hints <-chan struct{}, previous *platformState) (*platformState, error) {
	state := &platformState{links: make(map[int]link.Link), prefix: prefix, hints: hints, stop: make(chan struct{})}
	options := new(ebpf.CollectionOptions)
	if previous != nil {
		options.MapReplacements = map[string]*ebpf.Map{
			bpfMapForwardFlows: previous.objects.ForwardFlows,
			bpfMapReverseFlows: previous.objects.ReverseFlows,
		}
	}
	if err := loadBpfObjects(&state.objects, options); err != nil {
		return nil, fmt.Errorf("load service eBPF objects: %w", err)
	}
	if err := state.publish(tables); err != nil {
		_ = state.objects.Close()
		return nil, err
	}
	if err := state.refresh(); err != nil {
		_ = state.close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = state.close()
		return nil, err
	}
	state.done = make(chan struct{})
	go state.watch()
	return state, nil
}

// publish fills the inactive collection, so publication cannot expose partial service tables.
func (p *platformState) publish(tables Tables) error {
	if err := p.objects.Config.Put(uint32(0), tables.NodeIPv6); err != nil {
		return fmt.Errorf("publish node address: %w", err)
	}
	vips := make(map[[16]byte]struct{})
	for key, value := range tables.Services {
		kernelKey := bpfServiceKey{Vip: key.VIP, Port: bits.ReverseBytes16(key.Port), Protocol: uint8(key.Protocol)}
		if err := p.objects.Services.Put(kernelKey, bpfServiceValue{BackendOffset: value.BackendOffset, BackendCount: value.BackendCount}); err != nil {
			return fmt.Errorf("publish service: %w", err)
		}
		vips[key.VIP] = struct{}{}
	}
	for vip := range vips {
		if err := p.objects.ServiceVips.Put(vip, uint8(1)); err != nil {
			return fmt.Errorf("publish service VIP: %w", err)
		}
	}
	for index, backend := range tables.Backends {
		port := bits.ReverseBytes16(backend.Port)
		if err := p.objects.Backends.Put(uint32(index), bpfBackendValue{Address: backend.Address, Port: port}); err != nil {
			return fmt.Errorf("publish backend: %w", err)
		}
	}
	return nil
}

// watch periodically converges TCX links as host interfaces change.
func (p *platformState) watch() {
	defer close(p.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = p.refresh()
		case <-p.hints:
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
				_ = p.refresh()
			case <-p.stop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			}
		case <-p.stop:
			return
		}
	}
}

// refresh attaches ingress to Pod host routes and the primary IPv6 uplink.
func (p *platformState) refresh() error {
	indices, err := attachmentIndices("/proc/net/ipv6_route", p.prefix)
	if err != nil {
		return fmt.Errorf("discover dataplane interfaces: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	wanted := make(map[int]struct{}, len(indices))
	for _, index := range indices {
		wanted[index] = struct{}{}
		if existing := p.links[index]; existing != nil {
			info, infoErr := existing.Info()
			if infoErr == nil && info != nil && info.TCX() != nil && info.TCX().Ifindex == uint32(index) {
				continue
			}
			_ = existing.Close()
			delete(p.links, index)
		}
		attached, attachErr := link.AttachTCX(link.TCXOptions{Interface: index, Program: p.objects.PodminIngress, Attach: ebpf.AttachTCXIngress})
		if attachErr != nil {
			return fmt.Errorf("attach TCX ingress to interface %d: %w", index, attachErr)
		}
		info, infoErr := attached.Info()
		if infoErr != nil {
			_ = attached.Close()
			return fmt.Errorf("verify TCX ingress on interface %d: %w", index, infoErr)
		}
		if info == nil || info.TCX() == nil || info.TCX().Ifindex != uint32(index) {
			_ = attached.Close()
			return fmt.Errorf("verify TCX ingress on interface %d: wrong attachment", index)
		}
		p.links[index] = attached
	}
	for index, attached := range p.links {
		if _, keep := wanted[index]; !keep {
			_ = attached.Close()
			delete(p.links, index)
		}
	}
	return nil
}

// close detaches links and closes every unpinned eBPF object.
func (p *platformState) close() error {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	p.mu.Lock()
	var errs []error
	for _, attached := range p.links {
		errs = append(errs, attached.Close())
	}
	p.links = nil
	p.mu.Unlock()
	errs = append(errs, p.objects.Close())
	return errors.Join(errs...)
}

// attachmentIndices returns Pod host-route interfaces plus the lowest-metric default IPv6 route interface.
func attachmentIndices(routes string, prefix netip.Prefix) ([]int, error) {
	names, err := routeInterfaces(routes, prefix)
	if err != nil {
		return nil, err
	}
	indices := make(map[int]struct{})
	for _, name := range names {
		if name == "lo" {
			continue
		}
		iface, lookupErr := net.InterfaceByName(name)
		if lookupErr != nil {
			return nil, lookupErr
		}
		indices[iface.Index] = struct{}{}
	}
	result := make([]int, 0, len(indices))
	for index := range indices {
		result = append(result, index)
	}
	sort.Ints(result)
	return result, nil
}

// routeInterfaces reads Pod host routes and the lowest-metric default route from Linux's IPv6 route table.
func routeInterfaces(path string, prefix netip.Prefix) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	bestMetric := uint64(^uint32(0))
	best := ""
	names := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		isDefault := fields[0] == strings.Repeat("0", 32) && fields[1] == "00"
		isHost := fields[1] == "80"
		if !isDefault && !isHost {
			continue
		}
		if len(fields) < 10 {
			return nil, fmt.Errorf("malformed candidate IPv6 route: %q", scanner.Text())
		}
		if isDefault {
			metric, parseErr := strconv.ParseUint(fields[5], 16, 32)
			if parseErr != nil {
				return nil, fmt.Errorf("parse default IPv6 route metric %q: %w", fields[5], parseErr)
			}
			if metric < bestMetric {
				bestMetric, best = metric, fields[9]
			}
			continue
		}
		decoded, decodeErr := hex.DecodeString(fields[0])
		if decodeErr != nil {
			return nil, fmt.Errorf("parse host IPv6 route destination %q: %w", fields[0], decodeErr)
		}
		if len(decoded) != 16 {
			return nil, fmt.Errorf("parse host IPv6 route destination %q: wrong length", fields[0])
		}
		address, ok := netip.AddrFromSlice(decoded)
		if !ok {
			return nil, fmt.Errorf("parse host IPv6 route destination %q", fields[0])
		}
		if !prefix.Contains(address) {
			continue
		}
		if _, parseErr := strconv.ParseUint(fields[5], 16, 32); parseErr != nil {
			return nil, fmt.Errorf("parse host IPv6 route metric %q: %w", fields[5], parseErr)
		}
		names[fields[9]] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if best != "" {
		names[best] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
