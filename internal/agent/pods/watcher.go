// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package pods

import (
	"context"
	"io"
	"maps"
	"net"
	"net/netip"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	podsv1alpha1 "k8s.io/kubelet/pkg/apis/pods/v1alpha1"
)

// DefaultSocket is the kubelet PodsAPI Unix socket.
const DefaultSocket = "unix:///var/lib/kubelet/pods-api/pods-api.sock"

// Pod is the selector and readiness information owned by the watcher.
type Pod struct {
	UID       string
	Namespace string
	Labels    map[string]string
	Address   netip.Addr
	Eligible  bool
}

// Snapshot is an atomic, complete view keyed by Pod UID.
type Snapshot map[string]Pod

// Stream is the injectable subset of a PodsAPI watch stream.
type Stream interface {
	Recv() (*podsv1alpha1.WatchPodsEvent, error)
}

// Client is the injectable subset of the kubelet PodsAPI client.
type Client interface {
	Watch(context.Context) (Stream, error)
}

// GRPCClient adapts the generated kubelet client.
type GRPCClient struct{ Client podsv1alpha1.PodsClient }

// Watch starts a kubelet PodsAPI watch.
func (c GRPCClient) Watch(ctx context.Context) (Stream, error) {
	return c.Client.WatchPods(ctx, &podsv1alpha1.WatchPodsRequest{})
}

// Dial connects to the kubelet PodsAPI Unix socket with plaintext credentials.
func Dial(socket string) (*grpc.ClientConn, error) {
	if socket == "" {
		socket = DefaultSocket
	}
	return grpc.NewClient(socket, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(address, "unix://"))
	}))
}

// Watcher reconnects to PodsAPI and publishes complete snapshots.
type Watcher struct {
	Client  Client
	Publish func(Snapshot)
	Delay   time.Duration
}

// Run watches until context cancellation, publishing empty state on every disconnect.
func (w *Watcher) Run(ctx context.Context) error {
	delay := w.Delay
	if delay <= 0 || delay > 30*time.Second {
		delay = time.Second
	}
	for ctx.Err() == nil {
		stream, err := w.Client.Watch(ctx)
		if err == nil {
			err = w.consume(ctx, stream)
		}
		w.Publish(Snapshot{})
		if err == context.Canceled || ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

// consume stages initial pods and then publishes every complete update.
func (w *Watcher) consume(ctx context.Context, stream Stream) error {
	state := Snapshot{}
	synced := false
	for ctx.Err() == nil {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return io.EOF
			}
			return err
		}
		if event.Type == podsv1alpha1.EventType_INITIAL_SYNC_COMPLETE {
			synced = true
			w.Publish(clone(state))
			continue
		}
		var value corev1.Pod
		if err := value.Unmarshal(event.Pod); err != nil {
			return err
		}
		uid := string(value.UID)
		if event.Type == podsv1alpha1.EventType_DELETED {
			delete(state, uid)
		} else {
			state[uid] = project(&value)
		}
		if synced {
			w.Publish(clone(state))
		}
	}
	return ctx.Err()
}

// project reduces a Kubernetes Pod to watcher-owned state.
func project(value *corev1.Pod) Pod {
	namespace := value.Namespace
	if namespace == "" {
		namespace = "default"
	}
	result := Pod{UID: string(value.UID), Namespace: namespace, Labels: make(map[string]string, len(value.Labels))}
	for key, label := range value.Labels {
		result.Labels[key] = label
	}
	addresses := 0
	for _, raw := range value.Status.PodIPs {
		address, err := netip.ParseAddr(raw.IP)
		if err == nil && address.Is6() && address.IsGlobalUnicast() {
			result.Address = address
			addresses++
		}
	}
	if addresses != 1 {
		result.Address = netip.Addr{}
	}
	ready := false
	for _, condition := range value.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	result.Eligible = value.Status.Phase == corev1.PodRunning && value.DeletionTimestamp == nil && ready && result.Address.IsValid()
	return result
}

// clone protects the watcher's mutable state from callback ownership.
func clone(input Snapshot) Snapshot {
	output := make(Snapshot, len(input))
	for uid, pod := range input {
		pod.Labels = maps.Clone(pod.Labels)
		output[uid] = pod
	}
	return output
}
