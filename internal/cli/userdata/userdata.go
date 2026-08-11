// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package userdata

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed aws.sh
var userDataTemplate string

var heredocPattern = regexp.MustCompile(`<<-?[[:space:]]*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// Dependency is one architecture-specific object staged during first boot.
type Dependency struct {
	Name         string
	ObjectKey    string
	Digest       string
	Architecture string
}

// UserData contains values rendered into AWS cloud-init user-data.
type UserData struct {
	Bucket       string
	Region       string
	Cluster      string
	NodeGroup    string
	Architecture string
	PauseImage   string
	Dependencies []Dependency
}

// Render returns AWS cloud-init user-data containing only validated values.
func (u UserData) Render() ([]byte, error) {
	if err := safe("bucket", u.Bucket); err != nil {
		return nil, err
	}
	if !bucketPattern.MatchString(u.Bucket) {
		return nil, fmt.Errorf("invalid bucket %q", u.Bucket)
	}
	if err := safe("region", u.Region); err != nil {
		return nil, err
	}
	if !regionPattern.MatchString(u.Region) {
		return nil, fmt.Errorf("invalid region %q", u.Region)
	}
	if !idPattern.MatchString(u.Cluster) {
		return nil, fmt.Errorf("invalid cluster %q", u.Cluster)
	}
	if !idPattern.MatchString(u.NodeGroup) {
		return nil, fmt.Errorf("invalid nodegroup %q", u.NodeGroup)
	}
	if u.Architecture != "amd64" && u.Architecture != "arm64" {
		return nil, fmt.Errorf("unsupported architecture %q", u.Architecture)
	}
	if !imagePattern.MatchString(u.PauseImage) {
		return nil, fmt.Errorf("invalid pause image %q", u.PauseImage)
	}
	if len(u.Dependencies) == 0 {
		return nil, fmt.Errorf("dependencies are required")
	}

	rows := make([]string, 0, len(u.Dependencies))
	names := make(map[string]struct{}, len(u.Dependencies))
	for _, dependency := range u.Dependencies {
		if err := validateDependency(dependency); err != nil {
			return nil, err
		}
		if dependency.Architecture != u.Architecture {
			return nil, fmt.Errorf("dependency %q is for %s, not %s", dependency.Name, dependency.Architecture, u.Architecture)
		}
		if _, ok := names[dependency.Name]; ok {
			return nil, fmt.Errorf("duplicate dependency name %q", dependency.Name)
		}
		names[dependency.Name] = struct{}{}
		rows = append(rows, fmt.Sprintf("  '%s|%s|%s'", dependency.Name, dependency.ObjectKey, dependency.Digest))
	}
	for name := range requiredDependencies {
		if _, ok := names[name]; !ok {
			return nil, fmt.Errorf("dependency %q is required", name)
		}
	}

	replacements := strings.NewReplacer(
		"PODMIN_BUCKET", u.Bucket,
		"PODMIN_REGION", u.Region,
		"PODMIN_CLUSTER", u.Cluster,
		"PODMIN_NODEGROUP", u.NodeGroup,
		"PODMIN_ARCH", u.Architecture,
		"PODMIN_PAUSE_IMAGE", u.PauseImage,
		"  # PODMIN_DEPENDENCIES", strings.Join(rows, "\n"),
	)
	return []byte(replacements.Replace(userDataTemplate)), nil
}

// Compress returns comment-free, gzip-compressed rendered user-data.
func Compress(script []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(stripComments(script)); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

// stripComments removes shell comments and redundant blank lines outside heredocs.
func stripComments(script []byte) []byte {
	lines := strings.Split(string(script), "\n")
	result := make([]string, 0, len(lines))
	heredoc := ""
	blank := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if heredoc != "" {
			result = append(result, line)
			if trimmed == heredoc {
				heredoc = ""
			}
			continue
		}
		if match := heredocPattern.FindStringSubmatch(line); len(match) == 2 {
			heredoc = match[1]
		}
		if strings.HasPrefix(trimmed, "#") && (i != 0 || trimmed != "#!/usr/bin/env bash") {
			continue
		}
		if trimmed == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		result = append(result, strings.TrimRight(line, " \t"))
	}
	return []byte(strings.TrimSpace(strings.Join(result, "\n")) + "\n")
}
