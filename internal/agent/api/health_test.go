// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthHandlerPreservesBodies verifies healthy and unhealthy responses.
func TestHealthHandlerPreservesBodies(t *testing.T) {
	recorder := httptest.NewRecorder()
	NewHealthHandler(func() bool { return false }).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != "unhealthy\n" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}
