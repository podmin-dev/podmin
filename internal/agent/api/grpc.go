// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
)

// SyncBackend processes one client message and returns server messages to send.
type SyncBackend interface {
	Handle(context.Context, *ClientMessage) ([]*ServerMessage, error)
	Subscribe() (<-chan *ServerMessage, func())
}

// Server adapts a SyncBackend to the generated coordination server.
type Server struct {
	UnimplementedCoordinationServer
	Backend SyncBackend
}

// Sync serves one bidirectional coordination stream.
func (s *Server) Sync(stream grpc.BidiStreamingServer[ClientMessage, ServerMessage]) error {
	updates, unsubscribe := s.Backend.Subscribe()
	defer unsubscribe()
	session := ""
	incoming := make(chan *ClientMessage)
	receiveErrors := make(chan error, 1)
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			select {
			case incoming <- message:
			case <-stream.Context().Done():
				return
			}
		}
	}()
	for {
		select {
		case err := <-receiveErrors:
			if err == io.EOF {
				return nil
			}
			return err
		case update := <-updates:
			if session == "" {
				continue
			}
			if err := stream.Send(update); err != nil {
				return err
			}
		case message := <-incoming:
			switch {
			case message.GetHello() != nil:
				if session != "" || message.GetHello().SessionId == "" {
					return errors.New("coordination stream must contain exactly one initial hello")
				}
				session = message.GetHello().SessionId
			case message.GetNodeState() != nil:
				if session == "" || message.GetNodeState().SessionId != session {
					return errors.New("node state does not belong to this coordination stream")
				}
			case message.GetDrain() != nil:
				if session == "" || message.GetDrain().SessionId != session {
					return errors.New("drain does not belong to this coordination stream")
				}
			default:
				return errors.New("empty coordination stream message")
			}
			responses, err := s.Backend.Handle(stream.Context(), message)
			if err != nil {
				return err
			}
			for _, response := range responses {
				if err := stream.Send(response); err != nil {
					return err
				}
			}
		}
	}
}

// Follow opens Sync, sends outbound complete-state messages, and applies server messages.
func Follow(ctx context.Context, client CoordinationClient, outbound <-chan *ClientMessage, apply func(*ServerMessage) error) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Sync(streamCtx)
	if err != nil {
		return err
	}
	errSend := make(chan error, 1)
	go func() {
		for {
			var message *ClientMessage
			var open bool
			select {
			case message, open = <-outbound:
			case <-streamCtx.Done():
				return
			}
			if !open {
				err := stream.CloseSend()
				errSend <- err
				if err != nil {
					cancel()
				}
				return
			}
			if err := stream.Send(message); err != nil {
				errSend <- err
				cancel()
				return
			}
		}
	}()
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			select {
			case sendErr := <-errSend:
				return sendErr
			default:
			}
			return nil
		}
		if err != nil {
			select {
			case sendErr := <-errSend:
				return sendErr
			default:
			}
			return err
		}
		if err := apply(message); err != nil {
			return err
		}
	}
}
