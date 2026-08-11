// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/podmin-dev/podmin/internal/agent/api"
	"github.com/podmin-dev/podmin/internal/agent/dataplane"
	"github.com/podmin-dev/podmin/internal/agent/pods"
	"github.com/podmin-dev/podmin/internal/manifest"
	"google.golang.org/protobuf/proto"
)

// Dataplane is the complete-snapshot dataplane contract.
type Dataplane interface {
	Reconcile(context.Context, dataplane.Snapshot) error
}

// Controller combines desired Services with ready selected local Pods.
type Controller struct {
	Cluster, NodeGroup string
	NodeIPv6           netip.Addr
	DNS                *Server
	Dataplane          Dataplane
	applyMu            sync.Mutex
	mu                 sync.RWMutex
	services           []manifest.Service
	pods               pods.Snapshot
	subscribers        map[chan struct{}]struct{}
	health             atomic.Bool
}

// NewController constructs an initially healthy empty controller.
func NewController(cluster, nodeGroup string, nodeIPv6 netip.Addr, dns *Server, plane Dataplane) *Controller {
	value := &Controller{Cluster: cluster, NodeGroup: nodeGroup, NodeIPv6: nodeIPv6, DNS: dns, Dataplane: plane, pods: pods.Snapshot{}, subscribers: map[chan struct{}]struct{}{}}
	value.health.Store(true)
	return value
}

// Healthy reports whether the last required dataplane application succeeded.
func (c *Controller) Healthy() bool { return c.health.Load() }

// Subscribe returns an independent coalescing state-change channel and cleanup callback.
func (c *Controller) Subscribe() (<-chan struct{}, func()) {
	changed := make(chan struct{}, 1)
	c.mu.Lock()
	c.subscribers[changed] = struct{}{}
	c.mu.Unlock()
	return changed, func() {
		c.mu.Lock()
		delete(c.subscribers, changed)
		c.mu.Unlock()
	}
}

// SetServices replaces desired service configuration.
func (c *Controller) SetServices(value []manifest.Service) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	previousDigest := digestDesired(c.services)
	c.services = cloneDesired(value)
	changed := previousDigest != digestDesired(c.services)
	if len(value) == 0 {
		c.pods = pods.Snapshot{}
	}
	c.mu.Unlock()
	if changed {
		c.notify()
	}
}

// SetPods replaces the complete local pod view.
func (c *Controller) SetPods(value pods.Snapshot) {
	c.mu.Lock()
	c.pods = value
	c.mu.Unlock()
	c.notify()
}

// Digest returns a deterministic SHA-512 desired configuration digest.
func (c *Controller) Digest() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return digestDesired(c.services)
}

// Contract returns the complete local service contract without leased backends.
func (c *Controller) Contract() []*api.Service {
	c.mu.RLock()
	desired := cloneDesired(c.services)
	c.mu.RUnlock()
	return desiredContract(c.Cluster, c.NodeGroup, desired)
}

// digestDesired returns the deterministic SHA-512 digest for one owned desired snapshot.
func digestDesired(services []manifest.Service) string {
	sum := sha512.New()
	for _, service := range sortedDesired(services) {
		_, _ = fmt.Fprintf(sum, "%s\x00%s\x00", service.Namespace, service.Name)
		keys := make([]string, 0, len(service.Selector))
		for key := range service.Selector {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = fmt.Fprintf(sum, "%s=%s\x00", key, service.Selector[key])
		}
		for _, port := range service.Ports {
			_, _ = fmt.Fprintf(sum, "%s/%s/%d/%d\x00", port.Name, port.Protocol, port.Port, port.TargetPort)
		}
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// NodeState creates a complete ready-selected local endpoint view.
func (c *Controller) NodeState(session string, sequence uint64) *api.NodeState {
	c.mu.RLock()
	desired, current := cloneDesired(c.services), c.pods
	c.mu.RUnlock()
	state := &api.NodeState{SessionId: session, Sequence: sequence, ConfigDigest: digestDesired(desired)}
	for _, service := range sortedDesired(desired) {
		for _, pod := range current {
			if !pod.Eligible || pod.Namespace != service.Namespace || !matches(pod.Labels, service.Selector) {
				continue
			}
			endpoint := &api.Endpoint{Service: service.Name, Namespace: service.Namespace, NodeGroup: c.NodeGroup, PodUid: pod.UID, Address: pod.Address.String()}
			for _, port := range service.Ports {
				endpoint.Ports = append(endpoint.Ports, wirePort(port))
			}
			state.Endpoints = append(state.Endpoints, endpoint)
		}
	}
	sort.Slice(state.Endpoints, func(i, j int) bool {
		if state.Endpoints[i].Namespace != state.Endpoints[j].Namespace {
			return state.Endpoints[i].Namespace < state.Endpoints[j].Namespace
		}
		if state.Endpoints[i].Service != state.Endpoints[j].Service {
			return state.Endpoints[i].Service < state.Endpoints[j].Service
		}
		return state.Endpoints[i].PodUid < state.Endpoints[j].PodUid
	})
	return state
}

// ValidateNodeState rejects endpoints that do not exactly match current Service contracts.
func (c *Controller) ValidateNodeState(state *api.NodeState) error {
	return ValidateNodeState(c.NodeGroup, c.Contract(), state)
}

// ValidateNodeState rejects endpoints that do not exactly match the sending NodeGroup contract.
func ValidateNodeState(nodeGroup string, contract []*api.Service, state *api.NodeState) error {
	if state == nil || len(state.Endpoints) > 10000 {
		return errors.New("invalid endpoint count")
	}
	services := make(map[string]*api.Service, len(contract))
	for _, item := range contract {
		if item == nil {
			return errors.New("nil Service in contract")
		}
		for _, port := range item.Ports {
			if port == nil {
				return errors.New("nil target port in contract")
			}
		}
		for _, backend := range item.Backends {
			if backend == nil {
				return errors.New("nil backend in contract")
			}
			for _, port := range backend.Ports {
				if port == nil {
					return errors.New("nil target port in contract backend")
				}
			}
		}
		services[item.Namespace+"\x00"+item.Name] = item
	}
	seen := map[string]bool{}
	for _, endpoint := range state.Endpoints {
		if endpoint == nil {
			return errors.New("nil endpoint in node state")
		}
		for _, port := range endpoint.Ports {
			if port == nil {
				return errors.New("nil target port in node state")
			}
		}
		service, ok := services[endpoint.Namespace+"\x00"+endpoint.Service]
		address, err := netip.ParseAddr(endpoint.Address)
		identity := endpoint.Namespace + "\x00" + endpoint.Service + "\x00" + endpoint.PodUid + "\x00" + endpoint.Address
		if !ok || endpoint.NodeGroup != nodeGroup || endpoint.PodUid == "" || err != nil || !address.Is6() || !address.IsGlobalUnicast() || seen[identity] || len(endpoint.Ports) != len(service.Ports) {
			return errors.New("invalid service endpoint")
		}
		seen[identity] = true
		expected := map[string]bool{}
		for _, port := range service.Ports {
			expected[fmt.Sprintf("%s\x00%s\x00%d\x00%d", port.Name, port.Protocol, port.ServicePort, port.TargetPort)] = true
		}
		for _, port := range endpoint.Ports {
			key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", port.Name, port.Protocol, port.ServicePort, port.TargetPort)
			if !expected[key] {
				return errors.New("invalid service endpoint port")
			}
			delete(expected, key)
		}
	}
	return nil
}

// BuildSnapshot merges only node states matching the current desired digest.
func (c *Controller) BuildSnapshot(generation, revision uint64, leader string, nodes []*api.NodeState) *api.Snapshot {
	c.mu.RLock()
	desired := cloneDesired(c.services)
	c.mu.RUnlock()
	digest := digestDesired(desired)
	snapshot := &api.Snapshot{Generation: generation, Revision: revision, LeaderId: leader, ConfigDigest: digest}
	for _, wanted := range sortedDesired(desired) {
		item := &api.Service{Name: wanted.Name, Namespace: wanted.Namespace, NodeGroup: c.NodeGroup, Fqdn: wanted.Name + "." + wanted.Namespace + ".svc.cluster.local", Vip: dataplane.DeriveVIP(c.Cluster, wanted.Namespace, wanted.Name).String()}
		for _, port := range wanted.Ports {
			item.Ports = append(item.Ports, wirePort(port))
		}
		for _, node := range nodes {
			if node.ConfigDigest != digest {
				continue
			}
			for _, endpoint := range node.Endpoints {
				if endpoint.Namespace == wanted.Namespace && endpoint.Service == wanted.Name {
					item.Backends = append(item.Backends, endpoint)
				}
			}
		}
		sort.Slice(item.Backends, func(i, j int) bool {
			if item.Backends[i].Address != item.Backends[j].Address {
				return item.Backends[i].Address < item.Backends[j].Address
			}
			return item.Backends[i].PodUid < item.Backends[j].PodUid
		})
		snapshot.Services = append(snapshot.Services, item)
	}
	return snapshot
}

// Peer is one validated Hello contract and its optional leased endpoint state.
type Peer struct {
	Hello *api.Hello
	State *api.NodeState
}

// BuildClusterSnapshot merges NodeGroup contracts and matching leased node states.
func BuildClusterSnapshot(cluster string, generation, revision uint64, leader string, peers []Peer) (*api.Snapshot, error) {
	byNodeGroup := map[string]*api.Hello{}
	for _, peer := range peers {
		if peer.Hello != nil {
			for _, service := range peer.Hello.Services {
				if service == nil {
					return nil, errors.New("nil Service in Hello")
				}
				for _, port := range service.Ports {
					if port == nil {
						return nil, errors.New("nil target port in Hello Service")
					}
				}
				for _, backend := range service.Backends {
					if backend == nil {
						return nil, errors.New("nil backend in Hello Service")
					}
					for _, port := range backend.Ports {
						if port == nil {
							return nil, errors.New("nil target port in Hello backend")
						}
					}
				}
			}
			byNodeGroup[peer.Hello.NodeGroup] = peer.Hello
		}
		if peer.State != nil {
			for _, endpoint := range peer.State.Endpoints {
				if endpoint == nil {
					return nil, errors.New("nil endpoint in node state")
				}
				for _, port := range endpoint.Ports {
					if port == nil {
						return nil, errors.New("nil target port in endpoint")
					}
				}
			}
		}
	}
	nodeGroups := make([]string, 0, len(byNodeGroup))
	for nodeGroup := range byNodeGroup {
		nodeGroups = append(nodeGroups, nodeGroup)
	}
	sort.Strings(nodeGroups)
	snapshot := &api.Snapshot{Generation: generation, Revision: revision, LeaderId: leader}
	hash := sha512.New()
	identities := map[string]string{}
	for _, nodeGroup := range nodeGroups {
		hello := byNodeGroup[nodeGroup]
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", nodeGroup, hello.ConfigDigest)
		services := cloneServices(hello.Services)
		sort.Slice(services, func(i, j int) bool { return serviceLess(services[i], services[j]) })
		for _, item := range services {
			identity := item.Namespace + "\x00" + item.Name
			if owner, exists := identities[identity]; exists && owner != nodeGroup {
				return nil, fmt.Errorf("service %s/%s is owned by NodeGroups %q and %q", item.Namespace, item.Name, owner, nodeGroup)
			}
			identities[identity] = nodeGroup
			for _, peer := range peers {
				if peer.Hello == nil || peer.State == nil || peer.Hello.NodeGroup != nodeGroup || peer.State.ConfigDigest != hello.ConfigDigest {
					continue
				}
				for _, endpoint := range peer.State.Endpoints {
					if endpoint.NodeGroup == nodeGroup && endpoint.Namespace == item.Namespace && endpoint.Service == item.Name {
						item.Backends = append(item.Backends, proto.Clone(endpoint).(*api.Endpoint))
					}
				}
			}
			sort.Slice(item.Backends, func(i, j int) bool {
				if item.Backends[i].Address != item.Backends[j].Address {
					return item.Backends[i].Address < item.Backends[j].Address
				}
				return item.Backends[i].PodUid < item.Backends[j].PodUid
			})
			snapshot.Services = append(snapshot.Services, item)
		}
	}
	sort.Slice(snapshot.Services, func(i, j int) bool { return serviceLess(snapshot.Services[i], snapshot.Services[j]) })
	snapshot.ConfigDigest = hex.EncodeToString(hash.Sum(nil))
	return snapshot, nil
}

// ValidateSnapshotOwnership rejects duplicate cluster-wide Service identities.
func ValidateSnapshotOwnership(snapshot *api.Snapshot) error {
	if snapshot == nil {
		return errors.New("nil service snapshot")
	}
	identities := map[string]string{}
	for _, item := range snapshot.Services {
		if item == nil {
			return errors.New("nil Service in snapshot")
		}
		identity := item.Namespace + "\x00" + item.Name
		if owner, exists := identities[identity]; exists {
			return fmt.Errorf("service %s/%s is owned by NodeGroups %q and %q", item.Namespace, item.Name, owner, item.NodeGroup)
		}
		identities[identity] = item.NodeGroup
	}
	return nil
}

// Apply converts and applies one complete DNS and dataplane snapshot.
func (c *Controller) Apply(ctx context.Context, snapshot *api.Snapshot) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if snapshot == nil {
		return errors.New("nil service snapshot")
	}
	for _, service := range snapshot.Services {
		if service == nil {
			return errors.New("nil Service in snapshot")
		}
		for _, port := range service.Ports {
			if port == nil {
				return errors.New("nil target port in snapshot Service")
			}
		}
		for _, backend := range service.Backends {
			if backend == nil {
				return errors.New("nil backend in snapshot Service")
			}
			for _, port := range backend.Ports {
				if port == nil {
					return errors.New("nil target port in snapshot backend")
				}
			}
		}
	}
	local := c.Contract()
	localIdentities := make(map[string]*api.Service, len(local))
	for _, item := range local {
		localIdentities[item.Namespace+"\x00"+item.Name] = item
	}
	services := make([]*api.Service, 0, len(snapshot.Services)+len(local))
	for _, item := range snapshot.Services {
		identity := item.Namespace + "\x00" + item.Name
		wanted := localIdentities[identity]
		if item.NodeGroup == c.NodeGroup && wanted != nil {
			contract := proto.Clone(item).(*api.Service)
			contract.Backends = nil
			if proto.Equal(contract, wanted) {
				services = append(services, item)
				delete(localIdentities, identity)
			}
		} else if wanted == nil && item.NodeGroup != c.NodeGroup {
			services = append(services, item)
		}
	}
	for _, item := range localIdentities {
		services = append(services, item)
	}
	sort.Slice(services, func(i, j int) bool { return serviceLess(services[i], services[j]) })
	dnsValues := map[string][]net.IP{}
	model := dataplane.Snapshot{NodeIPv6: c.NodeIPv6}
	seenServices := map[string]bool{}
	for _, service := range services {
		vip, err := netip.ParseAddr(service.Vip)
		wantFQDN := service.Name + "." + service.Namespace + ".svc.cluster.local"
		identity := service.Namespace + "\x00" + service.Name
		if !manifest.ValidID(service.NodeGroup) || !manifest.ValidNamespace(service.Namespace) || !manifest.ValidID(service.Name) || seenServices[identity] || service.Fqdn != wantFQDN || err != nil || !vip.Is6() || vip != dataplane.DeriveVIP(c.Cluster, service.Namespace, service.Name) {
			return fmt.Errorf("invalid service VIP %q", service.Vip)
		}
		seenServices[identity] = true
		dnsValues[service.Fqdn] = []net.IP{net.IP(vip.AsSlice())}
		output := dataplane.Service{VIP: vip}
		seenPorts := map[string]bool{}
		for _, port := range service.Ports {
			if port.ServicePort == 0 || port.ServicePort > 65535 || port.TargetPort == 0 || port.TargetPort > 65535 || port.Protocol != "TCP" && port.Protocol != "UDP" {
				return errors.New("invalid service port")
			}
			portKey := fmt.Sprintf("%s/%d", port.Protocol, port.ServicePort)
			if seenPorts[portKey] {
				return errors.New("duplicate service port")
			}
			seenPorts[portKey] = true
			protocol := dataplane.ProtocolTCP
			if port.Protocol == "UDP" {
				protocol = dataplane.ProtocolUDP
			}
			item := dataplane.Port{Protocol: protocol, Port: uint16(port.ServicePort)}
			for _, backend := range service.Backends {
				address, err := netip.ParseAddr(backend.Address)
				if err != nil || !address.Is6() || !address.IsGlobalUnicast() || backend.PodUid == "" {
					return errors.New("invalid service backend")
				}
				matched := false
				for _, target := range backend.Ports {
					if target.Name == port.Name && target.Protocol == port.Protocol && target.ServicePort == port.ServicePort {
						if target.TargetPort == 0 || target.TargetPort > 65535 {
							return errors.New("invalid service backend port")
						}
						item.Backends = append(item.Backends, dataplane.Backend{Address: address, Port: uint16(target.TargetPort)})
						matched = true
					}
				}
				if !matched {
					return errors.New("service backend is missing a target port")
				}
			}
			output.Ports = append(output.Ports, item)
		}
		model.Services = append(model.Services, output)
	}
	if err := c.Dataplane.Reconcile(ctx, model); err != nil {
		c.health.Store(false)
		return err
	}
	c.DNS.Replace(dnsValues)
	c.health.Store(true)
	return nil
}

// ValidateContract validates a Hello's deterministic NodeGroup-owned Service identities and ports.
func ValidateContract(cluster, nodeGroup, digest string, services []*api.Service) error {
	if !manifest.ValidID(nodeGroup) || len(digest) != sha512.Size*2 || len(services) > 10000 {
		return errors.New("invalid service contract")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("invalid service contract digest")
	}
	seen := map[string]bool{}
	previous := ""
	for _, item := range services {
		if item == nil {
			return errors.New("nil Service in contract")
		}
		for _, backend := range item.Backends {
			if backend == nil {
				return errors.New("nil service contract backend")
			}
			for _, port := range backend.Ports {
				if port == nil {
					return errors.New("nil service contract backend port")
				}
			}
		}
		identity := item.Namespace + "\x00" + item.Name
		if item.NodeGroup != nodeGroup || !manifest.ValidNamespace(item.Namespace) || !manifest.ValidID(item.Name) || identity <= previous || seen[identity] || item.Fqdn != item.Name+"."+item.Namespace+".svc.cluster.local" || item.Vip != dataplane.DeriveVIP(cluster, item.Namespace, item.Name).String() || len(item.Backends) != 0 {
			return errors.New("invalid service contract identity")
		}
		previous, seen[identity] = identity, true
		ports := map[string]bool{}
		for _, port := range item.Ports {
			if port == nil {
				return errors.New("nil service contract port")
			}
			key := fmt.Sprintf("%s/%d", port.Protocol, port.ServicePort)
			if port.ServicePort == 0 || port.ServicePort > 65535 || port.TargetPort == 0 || port.TargetPort > 65535 || port.Protocol != "TCP" && port.Protocol != "UDP" || ports[key] {
				return errors.New("invalid service contract port")
			}
			ports[key] = true
		}
	}
	return nil
}

// desiredContract converts local manifests to a sorted wire contract.
func desiredContract(cluster, nodeGroup string, desired []manifest.Service) []*api.Service {
	out := make([]*api.Service, 0, len(desired))
	for _, wanted := range sortedDesired(desired) {
		item := &api.Service{Name: wanted.Name, Namespace: wanted.Namespace, NodeGroup: nodeGroup, Fqdn: wanted.Name + "." + wanted.Namespace + ".svc.cluster.local", Vip: dataplane.DeriveVIP(cluster, wanted.Namespace, wanted.Name).String()}
		for _, port := range wanted.Ports {
			item.Ports = append(item.Ports, wirePort(port))
		}
		out = append(out, item)
	}
	return out
}

// cloneServices returns ownership-safe service contracts without changing order.
func cloneServices(input []*api.Service) []*api.Service {
	out := make([]*api.Service, len(input))
	for i, item := range input {
		out[i] = proto.Clone(item).(*api.Service)
	}
	return out
}

// notify coalesces state-change notifications.
func (c *Controller) notify() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for changed := range c.subscribers {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

// matches implements exact Kubernetes matchLabels semantics.
func matches(labels, selector map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

// wirePort converts desired port metadata to transport form.
func wirePort(port manifest.ServicePort) *api.TargetPort {
	return &api.TargetPort{Name: port.Name, Protocol: port.Protocol, ServicePort: uint32(port.Port), TargetPort: uint32(port.TargetPort)}
}

// cloneDesired owns mutable desired state.
func cloneDesired(input []manifest.Service) []manifest.Service {
	out := make([]manifest.Service, len(input))
	for i, value := range input {
		out[i] = value
		out[i].Selector = map[string]string{}
		for key, item := range value.Selector {
			out[i].Selector[key] = item
		}
		out[i].Ports = append([]manifest.ServicePort(nil), value.Ports...)
	}
	return out
}

// sortedDesired returns a deterministic desired-service copy.
func sortedDesired(input []manifest.Service) []manifest.Service {
	out := cloneDesired(input)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// serviceLess orders wire Services by their cluster-wide Kubernetes identity.
func serviceLess(left, right *api.Service) bool {
	if left.Namespace != right.Namespace {
		return left.Namespace < right.Namespace
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.NodeGroup < right.NodeGroup
}
