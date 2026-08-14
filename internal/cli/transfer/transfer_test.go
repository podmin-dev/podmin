// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package transfer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/podmin-dev/podmin/internal/cli/tui"
)

// TestTransportAggregatesResponseBytes verifies registry bodies share one progress item.
func TestTransportAggregatesResponseBytes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "blob")
	}))
	defer server.Close()
	var events []tui.Event
	transport := &Transport{Name: "registry.example/image:tag", Progress: func(event tui.Event) { events = append(events, event) }}
	client := &http.Client{Transport: transport}
	for _, path := range []string{"manifest", "blob"} {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(response.Body); err != nil {
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	current, total := transport.Bytes()
	if len(events) < 2 || current != 8 || total != 8 || events[len(events)-1].Name != "registry.example/image:tag" || events[len(events)-1].Current != 8 {
		t.Fatalf("events = %#v", events)
	}
}
