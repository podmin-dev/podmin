// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package coordinator

import (
	"context"
	"crypto/rand"
	"net"
	"strings"
	"time"

	"github.com/podmin-dev/podmin/internal/agent/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// follow reconnects and sends Hello followed by immediate and periodic complete NodeState.
func (c *Coordinator) follow(ctx context.Context) {
	for ctx.Err() == nil {
		if c.config.Elector.IsLeader() {
			c.wait(ctx)
			continue
		}
		leader := strings.TrimPrefix(c.config.Elector.GetLeadershipStatus().LeaderAddr, "http://")
		if leader == "" {
			c.wait(ctx)
			continue
		}
		connection, err := c.dial(leader)
		if err == nil {
			session := rand.Text()
			outbound := make(chan *api.ClientMessage, 2)
			streamCtx, cancel := context.WithCancel(ctx)
			go c.sendStates(streamCtx, session, outbound)
			err = api.Follow(streamCtx, api.NewCoordinationClient(connection), outbound, func(message *api.ServerMessage) error {
				if value := message.GetSnapshot(); value != nil {
					return c.apply(ctx, value)
				}
				return nil
			})
			cancel()
			_ = connection.Close()
		}
		if err != nil && ctx.Err() == nil {
			c.config.Logger.Warn("service coordination stream failed", "error", err)
		}
		c.wait(ctx)
	}
}

// sendStates emits Hello and complete states immediately, on change, and every ten seconds.
func (c *Coordinator) sendStates(ctx context.Context, session string, outbound chan<- *api.ClientMessage) {
	defer close(outbound)
	changes, unsubscribe := c.config.Controller.Subscribe()
	defer unsubscribe()
	digest := c.config.Controller.Digest()
	select {
	case outbound <- &api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: c.hello(session, digest)}}:
	case <-ctx.Done():
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	sequence := uint64(0)
	for {
		sequence++
		state := c.config.Controller.NodeState(session, sequence)
		select {
		case outbound <- &api.ClientMessage{Message: &api.ClientMessage_NodeState{NodeState: state}}:
		case <-ctx.Done():
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-changes:
		}
	}
}

// drain best-effort removes the local node before lease expiry.
func (c *Coordinator) drain(ctx context.Context) {
	if !c.config.Elector.IsLeader() {
		leader := strings.TrimPrefix(c.config.Elector.GetLeadershipStatus().LeaderAddr, "http://")
		if leader == "" {
			return
		}
		connection, err := c.dial(leader)
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		stream, err := api.NewCoordinationClient(connection).Sync(ctx)
		if err != nil {
			return
		}
		session := rand.Text()
		_ = stream.Send(&api.ClientMessage{Message: &api.ClientMessage_Hello{Hello: c.hello(session, c.config.Controller.Digest())}})
		_ = stream.Send(&api.ClientMessage{Message: &api.ClientMessage_Drain{Drain: &api.Drain{SessionId: session}}})
		_ = stream.CloseSend()
		return
	}
	c.mu.Lock()
	for session, id := range c.sessions {
		if id == c.config.NodeID {
			delete(c.sessions, session)
			delete(c.nodes, id)
		}
	}
	c.mu.Unlock()
	_ = c.publish(ctx)
}

// hello constructs the canonical identity and IPv6 prefix report for every stream mode.
func (c *Coordinator) hello(session, digest string) *api.Hello {
	return &api.Hello{NodeId: c.config.NodeID, Cluster: c.config.Cluster, NodeGroup: c.config.NodeGroup, SessionId: session, ConfigDigest: digest, Services: c.config.Controller.Contract(), Ipv6Prefix: c.config.IPv6Prefix.String()}
}

// dial opens an mTLS connection that verifies the leader's advertised IP SAN.
func (c *Coordinator) dial(target string) (*grpc.ClientConn, error) {
	options := append([]grpc.DialOption(nil), c.config.DialOptions...)
	if c.config.ClientTLS != nil {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			return nil, err
		}
		configuration := c.config.ClientTLS.Clone()
		configuration.ServerName = strings.Trim(host, "[]")
		options = append(options, grpc.WithTransportCredentials(credentials.NewTLS(configuration)))
	}
	return grpc.NewClient(target, options...)
}

// wait delays reconnect while remaining cancellable.
func (c *Coordinator) wait(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
