// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"errors"
	"io/fs"
	"time"

	"github.com/podplane/s3lect"
)

// ErrNotFound identifies a missing object to generic callers and s3lect.
var ErrNotFound = errors.Join(fs.ErrNotExist, s3lect.ErrStorageNotFound)

// ErrExists identifies an object or secret that already exists.
var ErrExists = fs.ErrExist

// ErrPrecondition identifies a failed conditional object write.
var ErrPrecondition = s3lect.ErrStoragePrecondition

// ObjectInfo describes a listed object without its body.
type ObjectInfo struct {
	Key      string
	Modified time.Time
}
