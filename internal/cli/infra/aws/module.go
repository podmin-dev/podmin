// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import "embed"

// Module contains the conventional Terraform files shipped with Podmin.
//
//go:embed *.tf
var Module embed.FS
