// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package pods

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	podsv1alpha1 "k8s.io/kubelet/pkg/apis/pods/v1alpha1"
)

// fakeStream returns a fixed event sequence.
type fakeStream struct {
	events []*podsv1alpha1.WatchPodsEvent
}

// Recv returns the next event or EOF.
func (s *fakeStream) Recv() (*podsv1alpha1.WatchPodsEvent, error) {
	if len(s.events) == 0 {
		return nil, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

// fakeClient returns fixed streams and then waits for cancellation.
type fakeClient struct {
	mu      sync.Mutex
	streams []Stream
}

// Watch returns the next stream.
func (c *fakeClient) Watch(ctx context.Context) (Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.streams) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stream := c.streams[0]
	c.streams = c.streams[1:]
	return stream, nil
}

// podEvent serializes a Pod into a watch event.
func podEvent(t *testing.T, eventType podsv1alpha1.EventType, ready bool) *podsv1alpha1.WatchPodsEvent {
	t.Helper()
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID("uid-1"), Namespace: "product", Labels: map[string]string{"app": "api"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIPs: []corev1.PodIP{{IP: "2001:db8::10"}}, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}}}
	data, err := pod.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &podsv1alpha1.WatchPodsEvent{Type: eventType, Pod: data}
}

// TestConsumeStagesInitialSyncAndTracksTransitions verifies staging, readiness, and deletion.
func TestConsumeStagesInitialSyncAndTracksTransitions(t *testing.T) {
	var snapshots []Snapshot
	watcher := Watcher{Publish: func(snapshot Snapshot) { snapshots = append(snapshots, snapshot) }}
	stream := &fakeStream{events: []*podsv1alpha1.WatchPodsEvent{
		podEvent(t, podsv1alpha1.EventType_ADDED, false),
		{Type: podsv1alpha1.EventType_INITIAL_SYNC_COMPLETE},
		podEvent(t, podsv1alpha1.EventType_MODIFIED, true),
		podEvent(t, podsv1alpha1.EventType_DELETED, true),
	}}
	if err := watcher.consume(context.Background(), stream); err != io.EOF {
		t.Fatalf("consume error = %v", err)
	}
	if len(snapshots) != 3 || snapshots[0]["uid-1"].Eligible || !snapshots[1]["uid-1"].Eligible || snapshots[1]["uid-1"].Namespace != "product" || len(snapshots[2]) != 0 {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}
}

// TestRunPublishesEmptyOnDisconnect verifies fail-closed publication before reconnect.
func TestRunPublishesEmptyOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published := make(chan Snapshot, 2)
	watcher := Watcher{Client: &fakeClient{streams: []Stream{&fakeStream{events: []*podsv1alpha1.WatchPodsEvent{{Type: podsv1alpha1.EventType_INITIAL_SYNC_COMPLETE}}}}}, Publish: func(snapshot Snapshot) { published <- snapshot }, Delay: time.Millisecond}
	go func() { _ = watcher.Run(ctx) }()
	<-published
	snapshot := <-published
	if len(snapshot) != 0 {
		t.Fatalf("disconnect snapshot = %#v", snapshot)
	}
}

// TestProjectRejectsAmbiguousIPv6 verifies endpoint publication requires exactly one IPv6 address.
func TestProjectRejectsAmbiguousIPv6(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIPs: []corev1.PodIP{{IP: "2001:db8::1"}, {IP: "2001:db8::2"}}, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if got := project(pod); got.Eligible || got.Address.IsValid() {
		t.Fatalf("ambiguous Pod was eligible: %#v", got)
	}
}
