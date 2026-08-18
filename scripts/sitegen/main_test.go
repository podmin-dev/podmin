// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestExternalLinksOpenNewWindow verifies only absolute web links leave the documentation window.
func TestExternalLinksOpenNewWindow(t *testing.T) {
	source := []byte("[Kubernetes](https://kubernetes.io/docs/) [Guide](./docs/guide.md) [Section](#section) [Email](mailto:hello@example.com) <https://example.com>\n")
	var output bytes.Buffer
	if err := markdown.Convert(source, &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		`href="https://kubernetes.io/docs/" target="_blank" rel="noopener noreferrer"`,
		`href="https://example.com" target="_blank" rel="noopener noreferrer"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
	for _, want := range []string{
		`<a href="./docs/guide.md">Guide</a>`,
		`<a href="#section">Section</a>`,
		`<a href="mailto:hello@example.com">Email</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}
