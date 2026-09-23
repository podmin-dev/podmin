// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package userdata

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ValidateOTelLogs checks that OTLP values are safe to render into Bash and Fluent Bit configuration.
func ValidateOTelLogs(logs OTelLogs) error {
	for name, value := range map[string]string{"OTLP host": logs.Host, "OTLP port": logs.Port, "OTLP URI": logs.URI, "OTLP protocol": logs.Protocol} {
		if err := safe(name, value); err != nil {
			return err
		}
	}
	if net.ParseIP(logs.Host) == nil && !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`).MatchString(logs.Host) {
		return fmt.Errorf("invalid OTLP host %q", logs.Host)
	}
	port, err := strconv.Atoi(logs.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid OTLP port %q", logs.Port)
	}
	if !strings.HasPrefix(logs.URI, "/") || strings.ContainsAny(logs.URI, "?#") {
		return fmt.Errorf("invalid OTLP URI %q", logs.URI)
	}
	if logs.Protocol != "http/protobuf" && logs.Protocol != "grpc" {
		return fmt.Errorf("invalid OTLP protocol %q", logs.Protocol)
	}
	if logs.CA != "" {
		if err := validateS3Object("OTLP CA", logs.CA); err != nil {
			return err
		}
	}
	if logs.HeadersSecret != "" {
		if err := safe("OTLP headers secret", logs.HeadersSecret); err != nil {
			return err
		}
		if !strings.HasPrefix(logs.HeadersSecret, "/") || path.Clean(logs.HeadersSecret) != logs.HeadersSecret {
			return fmt.Errorf("invalid OTLP headers secret %q", logs.HeadersSecret)
		}
		if logs.HeadersProvider != "aws-parameter-store" && logs.HeadersProvider != "aws-secrets-manager" {
			return fmt.Errorf("invalid OTLP headers provider %q", logs.HeadersProvider)
		}
	} else if logs.HeadersProvider != "" {
		return errors.New("OTLP headers provider requires a headers secret")
	}
	return nil
}

// validateS3Object checks that value is an exact, shell-safe S3 object URL.
func validateS3Object(name, value string) error {
	object, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	key := strings.TrimPrefix(object.Path, "/")
	if object.Scheme != "s3" || object.User != nil || object.Host == "" || object.Host != object.Hostname() || object.RawQuery != "" || object.Fragment != "" || !bucketPattern.MatchString(object.Host) {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	if err = safe(name+" key", key); err != nil {
		return err
	}
	if path.Clean("/"+key) != "/"+key || strings.ContainsAny(key, `*?[]\`) {
		return fmt.Errorf("invalid %s key %q", name, key)
	}
	return nil
}

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
	"fluent-bit.deb":      {},
	"gvisor.tar.bz2":      {},
	"kubelet":             {},
	"libpq5.deb":          {},
	"podmin-agent.tar.gz": {},
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
