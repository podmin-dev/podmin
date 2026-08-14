// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package transfer

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/podmin-dev/podmin/internal/cli/tui"
)

const defaultConcurrency = 4

// Reader reports bytes read at a bounded interval.
type Reader struct {
	Reader   io.Reader
	Name     string
	Total    int64
	Offset   int64
	Progress tui.Progress
	current  int64
	last     time.Time
}

// Read implements io.Reader and emits byte progress.
func (r *Reader) Read(buffer []byte) (int, error) {
	count, err := r.Reader.Read(buffer)
	r.current += int64(count)
	if r.Progress != nil && (time.Since(r.last) >= 100*time.Millisecond || err != nil) {
		r.Progress(tui.Event{Type: tui.Progressed, Name: r.Name, Current: r.Offset + r.current, Total: r.Total})
		r.last = time.Now()
	}
	return count, err
}

// Seek rewinds seekable inputs for retries without double-counting progress.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := r.Reader.(io.Seeker)
	if !ok {
		return 0, errors.New("transfer input is not seekable")
	}
	position, err := seeker.Seek(offset, whence)
	if err == nil {
		r.current = position
	}
	return position, err
}

// Transport aggregates successful HTTP response bodies into one download item.
type Transport struct {
	Base     http.RoundTripper
	Name     string
	Progress tui.Progress
	mu       sync.Mutex
	current  int64
	total    int64
	once     sync.Once
	slots    chan struct{}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.once.Do(func() { t.slots = make(chan struct{}, defaultConcurrency) })
	select {
	case t.slots <- struct{}{}:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	if err != nil || request.Method != http.MethodGet || response.StatusCode < 200 || response.StatusCode >= 300 || response.Body == nil {
		<-t.slots
		return response, err
	}
	t.mu.Lock()
	if response.ContentLength > 0 {
		t.total += response.ContentLength
	}
	t.mu.Unlock()
	response.Body = &transportBody{ReadCloser: response.Body, transport: t, release: func() { <-t.slots }}
	return response, nil
}

// Bytes returns the aggregate bytes read and expected by the transport.
func (t *Transport) Bytes() (int64, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current, max(t.current, t.total)
}

// transportBody reports bytes read from one response to its aggregate transport.
type transportBody struct {
	io.ReadCloser
	transport *Transport
	last      time.Time
	release   func()
	once      sync.Once
}

// Read implements io.Reader.
func (b *transportBody) Read(buffer []byte) (int, error) {
	count, err := b.ReadCloser.Read(buffer)
	b.transport.mu.Lock()
	b.transport.current += int64(count)
	current, total := b.transport.current, max(b.transport.current, b.transport.total)
	b.transport.mu.Unlock()
	if b.transport.Progress != nil && (time.Since(b.last) >= 100*time.Millisecond || err != nil) {
		b.transport.Progress(tui.Event{Type: tui.Progressed, Name: b.transport.Name, Current: current, Total: total})
		b.last = time.Now()
	}
	return count, err
}

// Close closes the response body and releases its download slot.
func (b *transportBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
