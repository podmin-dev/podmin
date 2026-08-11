// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package coordinator

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/api"
	"github.com/podmin-dev/podmin/internal/agent/identity"
	"github.com/podmin-dev/podmin/internal/agent/service"
	"github.com/podplane/s3lect"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const snapshotKey = "dns/services.pb"

// Config configures complete-state service coordination.
type Config struct {
	NodeID, Cluster, NodeGroup string
	Elector                    s3lect.Elector
	Storage                    s3lect.Storage
	Controller                 *service.Controller
	Logger                     *slog.Logger
	DialOptions                []grpc.DialOption
	ClientTLS                  *tls.Config
	IPv6Prefix                 netip.Prefix
}

// receivedNode is a complete node view and leader-local lease time.
type receivedNode struct {
	hello    *api.Hello
	state    *api.NodeState
	received time.Time
	prefix   netip.Prefix
}

// Coordinator elects one service manager and distributes complete snapshots.
type Coordinator struct {
	config        Config
	publishMu     sync.Mutex
	applyMu       sync.Mutex
	mu            sync.Mutex
	nodes         map[string]receivedNode
	sessions      map[string]string
	snapshot      *api.Snapshot
	generation    uint64
	wasLeader     bool
	leaderReady   atomic.Bool
	lastSnapshot  time.Time
	staleCleared  bool
	subscribers   map[chan *api.ServerMessage]struct{}
	fatal         chan error
	localSession  string
	localSequence uint64
}

// New validates configuration and creates a coordinator.
func New(config Config) (*Coordinator, error) {
	if config.NodeID == "" || config.Cluster == "" || config.NodeGroup == "" || config.Elector == nil || config.Storage == nil || config.Controller == nil || !validDelegatedPrefix(config.IPv6Prefix) {
		return nil, errors.New("complete service coordinator configuration is required")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Coordinator{config: config, nodes: map[string]receivedNode{}, sessions: map[string]string{}, subscribers: map[chan *api.ServerMessage]struct{}{}, fatal: make(chan error, 1)}, nil
}

// validDelegatedPrefix reports whether a prefix is canonical global-unicast IPv6 /80.
func validDelegatedPrefix(prefix netip.Prefix) bool {
	return prefix.IsValid() && prefix.Addr().Is6() && !prefix.Addr().Is4In6() && prefix.Addr().IsGlobalUnicast() && prefix.Bits() == 80 && prefix == prefix.Masked()
}

// LoadSnapshot loads and applies the last durable complete service view.
func (c *Coordinator) LoadSnapshot(ctx context.Context) error {
	body, _, err := c.config.Storage.Get(ctx, snapshotKey)
	if errors.Is(err, s3lect.ErrStorageNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	value := new(api.Snapshot)
	if err = proto.Unmarshal(body, value); err != nil {
		return fmt.Errorf("decode durable service snapshot: %w", err)
	}
	// Durable service identities are safe to restore for DNS, but endpoint
	// readiness is a lease and must be re-established by live node streams.
	for _, service := range value.Services {
		service.Backends = nil
	}
	// The durable global view may predate local reconciliation. Never restore
	// a stale contract for this node's own NodeGroup.
	services := value.Services[:0]
	for _, item := range value.Services {
		if item.NodeGroup != c.config.NodeGroup {
			services = append(services, item)
		}
	}
	value.Services = append(services, c.config.Controller.Contract()...)
	sort.Slice(value.Services, func(i, j int) bool {
		return serviceIdentityLess(value.Services[i], value.Services[j])
	})
	return c.apply(ctx, value)
}

// Run starts election and maintains a stream to the elected leader.
func (c *Coordinator) Run(ctx context.Context) error {
	if err := c.config.Elector.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = c.config.Elector.Stop() }()
	changes, unsubscribe := c.config.Controller.Subscribe()
	defer unsubscribe()
	lastDesired := ""
	for {
		desired := c.config.Controller.Digest()
		if err := c.applyLocalTransition(ctx); err != nil {
			return fmt.Errorf("apply initial local service state: %w", err)
		}
		if desired == c.config.Controller.Digest() {
			lastDesired = desired
			break
		}
	}
	go c.follow(ctx)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLocal := time.Time{}
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.drain(drainCtx)
			cancel()
			return nil
		case err := <-c.fatal:
			return err
		case <-changes:
			currentDesired := c.config.Controller.Digest()
			if currentDesired != lastDesired {
				if err := c.applyLocalTransition(ctx); err != nil {
					return fmt.Errorf("apply local service transition: %w", err)
				}
				lastDesired = currentDesired
			}
			if c.config.Elector.IsLeader() {
				_ = c.registerLocal(ctx)
				lastLocal = time.Now()
			}
		case <-ticker.C:
			leader := c.config.Elector.IsLeader()
			if leader && !c.wasLeader {
				if err := c.acquire(ctx); err != nil {
					c.config.Logger.Warn("acquire service generation", "error", err)
				} else {
					c.wasLeader = true
					_ = c.registerLocal(ctx)
					lastLocal = time.Now()
				}
			}
			if !leader {
				c.wasLeader = false
				c.leaderReady.Store(false)
				c.clearStale(ctx, time.Now())
			}
			if leader && time.Since(lastLocal) >= 10*time.Second {
				_ = c.registerLocal(ctx)
				lastLocal = time.Now()
			}
			if leader {
				_ = c.expireAndPublish(ctx)
			}
		}
	}
}

// registerLocal refreshes the leader node through the same validated complete-state path.
func (c *Coordinator) registerLocal(ctx context.Context) error {
	c.mu.Lock()
	if c.localSession == "" {
		c.localSession = rand.Text()
	}
	session := c.localSession
	c.localSequence++
	sequence := c.localSequence
	c.mu.Unlock()
	digest := c.config.Controller.Digest()
	if _, err := c.handle(ctx, &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: c.hello(session, digest)}}, true); err != nil {
		return err
	}
	_, err := c.handle(ctx, &api.ClientMessage{Message: &api.ClientMessage_NodeState{NodeState: c.config.Controller.NodeState(session, sequence)}}, true)
	return err
}

// acquire CAS-increments the durable generation for a newly observed leadership acquisition.
func (c *Coordinator) acquire(ctx context.Context) error {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	for attempts := 0; attempts < 5; attempts++ {
		body, etag, err := c.config.Storage.Get(ctx, snapshotKey)
		current := new(api.Snapshot)
		if errors.Is(err, s3lect.ErrStorageNotFound) {
			etag, err = "", nil
		}
		if err != nil {
			return err
		}
		if len(body) > 0 {
			if err = proto.Unmarshal(body, current); err != nil {
				return err
			}
		}
		for _, item := range current.Services {
			item.Backends = nil
		}
		candidate := c.overlayLocalContract(current)
		candidate.Generation = current.Generation + 1
		candidate.Revision = 0
		candidate.LeaderId = c.config.NodeID
		if err = service.ValidateSnapshotOwnership(candidate); err != nil {
			return err
		}
		encoded, err := deterministic(candidate)
		if err != nil {
			return err
		}
		if err = c.config.Storage.PutIfMatch(ctx, snapshotKey, encoded, etag); errors.Is(err, s3lect.ErrStoragePrecondition) {
			continue
		}
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.generation = candidate.Generation
		c.nodes = map[string]receivedNode{}
		c.sessions = map[string]string{}
		c.mu.Unlock()
		if err := c.apply(ctx, candidate); err != nil {
			return err
		}
		c.leaderReady.Store(true)
		return nil
	}
	return errors.New("service generation CAS retries exhausted")
}

// Handle validates and processes one complete-state gRPC message.
func (c *Coordinator) Handle(ctx context.Context, message *api.ClientMessage) ([]*api.ServerMessage, error) {
	return c.handle(ctx, message, false)
}

// handle processes a message, with an explicit bypass reserved for direct leader-local calls.
func (c *Coordinator) handle(ctx context.Context, message *api.ClientMessage, local bool) ([]*api.ServerMessage, error) {
	if !c.config.Elector.IsLeader() {
		return nil, errors.New("not service leader")
	}
	if !c.leaderReady.Load() {
		return nil, errors.New("service leader is initializing")
	}
	if hello := message.GetHello(); hello != nil {
		if hello.NodeId == "" || hello.Cluster != c.config.Cluster || hello.SessionId == "" || service.ValidateContract(c.config.Cluster, hello.NodeGroup, hello.ConfigDigest, hello.Services) != nil {
			return nil, errors.New("invalid hello")
		}
		if local && hello.Ipv6Prefix == "" {
			hello.Ipv6Prefix = c.config.IPv6Prefix.String()
		}
		prefix, err := netip.ParsePrefix(hello.Ipv6Prefix)
		if err != nil || !validDelegatedPrefix(prefix) {
			return nil, errors.New("invalid delegated IPv6 prefix")
		}
		if !local {
			peerIdentity, identityErr := identity.IdentityFromContext(ctx, c.config.Cluster)
			if identityErr != nil || peerIdentity.NodeID != hello.NodeId || peerIdentity.NodeGroup != hello.NodeGroup {
				return nil, errors.New("hello does not match authenticated node identity")
			}
		} else if prefix != c.config.IPv6Prefix {
			return nil, errors.New("invalid local hello prefix")
		}
		c.mu.Lock()
		for id, peer := range c.nodes {
			if id != hello.NodeId && peer.hello != nil && peer.hello.NodeGroup == hello.NodeGroup && (peer.hello.ConfigDigest != hello.ConfigDigest || !proto.Equal(&api.Hello{Services: peer.hello.Services}, &api.Hello{Services: hello.Services})) {
				c.mu.Unlock()
				return nil, errors.New("conflicting service contract within NodeGroup")
			}
			if id != hello.NodeId && peer.hello != nil && peer.hello.NodeGroup != hello.NodeGroup && contractsOverlap(peer.hello.Services, hello.Services) {
				c.mu.Unlock()
				return nil, errors.New("conflicting Service ownership across NodeGroups")
			}
		}
		if previous, exists := c.nodes[hello.NodeId]; exists && previous.hello != nil {
			delete(c.sessions, previous.hello.SessionId)
		}
		c.sessions[hello.SessionId] = hello.NodeId
		c.nodes[hello.NodeId] = receivedNode{hello: proto.Clone(hello).(*api.Hello), received: time.Now(), prefix: prefix}
		snapshot := cloneSnapshot(c.snapshot)
		c.mu.Unlock()
		return snapshotMessage(snapshot), nil
	}
	if state := message.GetNodeState(); state != nil {
		c.mu.Lock()
		id := c.sessions[state.SessionId]
		old, ok := c.nodes[id]
		if id == "" || !ok || old.hello == nil || old.hello.SessionId != state.SessionId {
			c.mu.Unlock()
			return nil, errors.New("invalid node state session or config digest")
		}
		if old.hello.ConfigDigest != state.ConfigDigest {
			delete(c.sessions, state.SessionId)
			delete(c.nodes, id)
			c.mu.Unlock()
			if err := c.publish(ctx); err != nil {
				return nil, fmt.Errorf("publish retired node configuration: %w", err)
			}
			return nil, errors.New("node configuration changed; reconnect required")
		}
		if err := service.ValidateNodeState(old.hello.NodeGroup, old.hello.Services, state); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		for _, endpoint := range state.Endpoints {
			address, err := netip.ParseAddr(endpoint.Address)
			if err != nil || !old.prefix.Contains(address) {
				c.mu.Unlock()
				return nil, errors.New("endpoint is outside reported delegated prefix")
			}
		}
		if old.state != nil && state.Sequence <= old.state.Sequence {
			snapshot := cloneSnapshot(c.snapshot)
			c.mu.Unlock()
			return append([]*api.ServerMessage{{Message: &api.ServerMessage_Ack{Ack: &api.Ack{Sequence: state.Sequence}}}}, snapshotMessage(snapshot)...), nil
		}
		old.state = proto.Clone(state).(*api.NodeState)
		old.received = time.Now()
		c.nodes[id] = old
		c.mu.Unlock()
		if err := c.publish(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		snapshot := cloneSnapshot(c.snapshot)
		c.mu.Unlock()
		return append([]*api.ServerMessage{{Message: &api.ServerMessage_Ack{Ack: &api.Ack{Sequence: state.Sequence}}}}, snapshotMessage(snapshot)...), nil
	}
	if drain := message.GetDrain(); drain != nil {
		c.mu.Lock()
		id := c.sessions[drain.SessionId]
		delete(c.sessions, drain.SessionId)
		if current, exists := c.nodes[id]; exists && current.hello != nil && current.hello.SessionId == drain.SessionId {
			delete(c.nodes, id)
		}
		c.mu.Unlock()
		return nil, c.publish(ctx)
	}
	return nil, errors.New("empty coordination message")
}

// Subscribe receives latest complete snapshots published after subscription.
func (c *Coordinator) Subscribe() (<-chan *api.ServerMessage, func()) {
	ch := make(chan *api.ServerMessage, 1)
	c.mu.Lock()
	c.subscribers[ch] = struct{}{}
	snapshot := cloneSnapshot(c.snapshot)
	c.mu.Unlock()
	for _, message := range snapshotMessage(snapshot) {
		ch <- message
	}
	return ch, func() { c.mu.Lock(); delete(c.subscribers, ch); c.mu.Unlock() }
}

// expireAndPublish expires leader-receipt leases and publishes only after a change.
func (c *Coordinator) expireAndPublish(ctx context.Context) error {
	c.mu.Lock()
	changed := false
	now := time.Now()
	for id, node := range c.nodes {
		if !node.received.IsZero() && now.Sub(node.received) > 35*time.Second {
			delete(c.sessions, node.hello.SessionId)
			delete(c.nodes, id)
			changed = true
		}
	}
	c.mu.Unlock()
	if changed {
		return c.publish(ctx)
	}
	return nil
}

// publish builds, fences, persists, applies, and broadcasts one complete snapshot.
func (c *Coordinator) publish(ctx context.Context) error {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	if !c.config.Elector.IsLeader() {
		return errors.New("leadership lost")
	}
	c.mu.Lock()
	generation := c.generation
	revision := uint64(1)
	if c.snapshot != nil && c.snapshot.Generation == generation {
		revision = c.snapshot.Revision + 1
	}
	nodes := make([]receivedNode, 0, len(c.nodes))
	for _, n := range c.nodes {
		nodes = append(nodes, n)
	}
	c.mu.Unlock()
	peers := make([]service.Peer, 0, len(nodes))
	for _, node := range nodes {
		peers = append(peers, service.Peer{Hello: node.hello, State: node.state})
	}
	snapshot, err := service.BuildClusterSnapshot(c.config.Cluster, generation, revision, c.config.NodeID, peers)
	if err != nil {
		return err
	}
	body, etag, err := c.config.Storage.Get(ctx, snapshotKey)
	if err != nil {
		return err
	}
	durable := new(api.Snapshot)
	if err = proto.Unmarshal(body, durable); err != nil {
		return err
	}
	if durable.Generation != generation || durable.LeaderId != c.config.NodeID {
		return errors.New("durable service leader superseded this process")
	}
	if durable.Revision+1 != revision {
		return errors.New("durable service revision changed during publication")
	}
	encoded, err := deterministic(snapshot)
	if err != nil {
		return err
	}
	if !c.config.Elector.IsLeader() {
		return errors.New("leadership lost")
	}
	if err = c.config.Storage.PutIfMatch(ctx, snapshotKey, encoded, etag); err != nil {
		return err
	}
	if err = c.apply(ctx, snapshot); err != nil {
		return err
	}
	c.broadcast(snapshot)
	return nil
}

// apply atomically applies a monotonic complete snapshot to DNS and dataplane.
func (c *Coordinator) apply(ctx context.Context, snapshot *api.Snapshot) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	if c.snapshot != nil && (snapshot.Generation < c.snapshot.Generation || snapshot.Generation == c.snapshot.Generation && snapshot.Revision <= c.snapshot.Revision) {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if err := c.config.Controller.Apply(ctx, snapshot); err != nil {
		if len(snapshot.Services) != 0 {
			select {
			case c.fatal <- fmt.Errorf("apply service dataplane: %w", err):
			default:
			}
		}
		return err
	}
	c.mu.Lock()
	c.snapshot = cloneSnapshot(snapshot)
	c.lastSnapshot = time.Now()
	c.staleCleared = false
	c.mu.Unlock()
	return nil
}

// clearStale removes leased backends after the follower loses fresh snapshots.
func (c *Coordinator) clearStale(ctx context.Context, now time.Time) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	if c.staleCleared || c.snapshot == nil || c.lastSnapshot.IsZero() || now.Sub(c.lastSnapshot) <= 35*time.Second {
		c.mu.Unlock()
		return
	}
	snapshot := cloneSnapshot(c.snapshot)
	for _, item := range snapshot.Services {
		item.Backends = nil
	}
	c.mu.Unlock()
	snapshot = c.overlayLocalContract(snapshot)
	if err := c.config.Controller.Apply(ctx, snapshot); err != nil {
		c.config.Logger.Warn("clear stale service backends", "error", err)
		return
	}
	c.mu.Lock()
	c.staleCleared = true
	c.mu.Unlock()
}

// applyLocalTransition replaces only this NodeGroup's identities and withdraws its backends immediately.
func (c *Coordinator) applyLocalTransition(ctx context.Context) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	snapshot := cloneSnapshot(c.snapshot)
	stale := c.staleCleared
	c.mu.Unlock()
	if snapshot == nil {
		snapshot = &api.Snapshot{}
	}
	if stale {
		for _, item := range snapshot.Services {
			item.Backends = nil
		}
	}
	return c.config.Controller.Apply(ctx, c.overlayLocalContract(snapshot))
}

// overlayLocalContract preserves remote NodeGroups while replacing the current local contract without backends.
func (c *Coordinator) overlayLocalContract(snapshot *api.Snapshot) *api.Snapshot {
	local := c.config.Controller.Contract()
	identities := make(map[string]bool, len(local))
	for _, item := range local {
		identities[item.Namespace+"\x00"+item.Name] = true
	}
	services := snapshot.Services[:0]
	for _, item := range snapshot.Services {
		if item.NodeGroup != c.config.NodeGroup && !identities[item.Namespace+"\x00"+item.Name] {
			services = append(services, item)
		}
	}
	snapshot.Services = append(services, local...)
	sort.Slice(snapshot.Services, func(i, j int) bool {
		return serviceIdentityLess(snapshot.Services[i], snapshot.Services[j])
	})
	return snapshot
}

// contractsOverlap reports whether two contracts claim the same global Service identity.
func contractsOverlap(left, right []*api.Service) bool {
	identities := make(map[string]bool, len(left))
	for _, item := range left {
		identities[item.Namespace+"\x00"+item.Name] = true
	}
	for _, item := range right {
		if identities[item.Namespace+"\x00"+item.Name] {
			return true
		}
	}
	return false
}

// serviceIdentityLess orders Services by namespace and name.
func serviceIdentityLess(left, right *api.Service) bool {
	if left.Namespace != right.Namespace {
		return left.Namespace < right.Namespace
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.NodeGroup < right.NodeGroup
}

// broadcast replaces queued messages so slow streams receive latest-complete semantics.
func (c *Coordinator) broadcast(snapshot *api.Snapshot) {
	message := snapshotMessage(snapshot)[0]
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subscribers {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- message:
		default:
		}
	}
}

// deterministic marshals protobuf with stable field ordering.
func deterministic(message proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

// cloneSnapshot returns an ownership-safe protobuf clone.
func cloneSnapshot(value *api.Snapshot) *api.Snapshot {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*api.Snapshot)
}

// snapshotMessage wraps a non-nil snapshot for transport.
func snapshotMessage(value *api.Snapshot) []*api.ServerMessage {
	if value == nil {
		return nil
	}
	return []*api.ServerMessage{{Message: &api.ServerMessage_Snapshot{Snapshot: value}}}
}
