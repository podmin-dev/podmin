// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/podplane/s3lect"
)

// TestObjectErrorsMatchConsumers verifies generic and s3lect error contracts.
func TestObjectErrorsMatchConsumers(t *testing.T) {
	if !errors.Is(ErrNotFound, fs.ErrNotExist) {
		t.Fatal("cloud not-found error does not match fs.ErrNotExist")
	}
	if !errors.Is(ErrNotFound, s3lect.ErrStorageNotFound) {
		t.Fatal("cloud not-found error does not match s3lect")
	}
	if !errors.Is(ErrPrecondition, s3lect.ErrStoragePrecondition) {
		t.Fatal("cloud precondition error does not match s3lect")
	}
	if !errors.Is(ErrExists, fs.ErrExist) {
		t.Fatal("cloud exists error does not match fs.ErrExist")
	}
}
