// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// eofCoordinationServer immediately closes every coordination stream.
type eofCoordinationServer struct {
	UnimplementedCoordinationServer
}

// Sync closes the stream without waiting for a client message.
func (eofCoordinationServer) Sync(grpc.BidiStreamingServer[ClientMessage, ServerMessage]) error {
	return nil
}

// TestFollowReturnsWhenServerClosesWithOpenOutbound verifies an idle sender is canceled after server EOF.
func TestFollowReturnsWhenServerClosesWithOpenOutbound(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	RegisterCoordinationServer(server, eofCoordinationServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufconn", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	done := make(chan error, 1)
	go func() {
		done <- Follow(context.Background(), NewCoordinationClient(connection), make(chan *ClientMessage), func(*ServerMessage) error { return nil })
	}()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Follow did not return after the server closed the stream")
	}
}
