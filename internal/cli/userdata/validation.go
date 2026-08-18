// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package userdata

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	digestPattern = regexp.MustCompile(`^sha(256:[[:xdigit:]]{64}|512:[[:xdigit:]]{128})$`)
	idPattern     = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)
	imagePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]+$`)
)

var requiredDependencies = map[string]struct{}{
	"cni-plugins.tar.gz":  {},
	"containerd.tar.gz":   {},
	"coredns.tar.gz":      {},
	"crictl.tar.gz":       {},
	"gvisor.tar.bz2":      {},
	"kubelet":             {},
	"podmin-agent.tar.gz": {},
	"zot":                 {},
}

// validateDependency checks that a dependency is safe to render into Bash.
func validateDependency(dependency Dependency) error {
	if err := safe("dependency name", dependency.Name); err != nil {
		return err
	}
	if !namePattern.MatchString(dependency.Name) || path.Base(dependency.Name) != dependency.Name || dependency.Name == "." || dependency.Name == ".." {
		return fmt.Errorf("invalid dependency name %q", dependency.Name)
	}
	if err := safe("dependency object key", dependency.ObjectKey); err != nil {
		return err
	}
	if !strings.HasPrefix(dependency.ObjectKey, "dependencies/") || path.Clean(dependency.ObjectKey) != dependency.ObjectKey || strings.ContainsAny(dependency.ObjectKey, `*?[]\`) {
		return fmt.Errorf("invalid dependency object key %q", dependency.ObjectKey)
	}
	if !digestPattern.MatchString(dependency.Digest) {
		return fmt.Errorf("invalid dependency digest %q", dependency.Digest)
	}
	return nil
}

// safe rejects empty values and shell delimiters used by the template.
func safe(name, value string) error {
	if value == "" || strings.ContainsAny(value, "'|\n\r\x00") {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}
