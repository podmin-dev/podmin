// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const host = "registry.podmin.internal"
const appNamespace = "apps/"
const mirrorNamespace = "mirror/"

// App returns an application reference from a source and optional destination.
func App(source name.Tag, destination string) (name.Tag, error) {
	value := strings.TrimSpace(destination)
	if value == "" {
		value = fmt.Sprintf("%s/%s%s/%s:%s", host, appNamespace, source.RegistryStr(), source.Context().RepositoryStr(), source.TagStr())
	} else if shorthand(value) {
		value = host + "/" + appNamespace + strings.TrimPrefix(value, appNamespace)
	}
	ref, err := tag(value)
	if err != nil {
		return name.Tag{}, fmt.Errorf("parse image destination: %w", err)
	}
	if ref.RegistryStr() != host || !strings.HasPrefix(ref.Context().RepositoryStr(), appNamespace) {
		return name.Tag{}, fmt.Errorf("destination must be under %s/%s", host, appNamespace)
	}
	return ref, nil
}

// Parse normalizes shorthand and validates a cluster image reference.
func Parse(value string) (name.Tag, error) {
	value = strings.TrimSpace(value)
	if shorthand(value) {
		value = host + "/" + appNamespace + strings.TrimPrefix(value, appNamespace)
	}
	ref, err := tag(value)
	if err != nil {
		return name.Tag{}, fmt.Errorf("parse image reference: %w", err)
	}
	repository := ref.Context().RepositoryStr()
	if ref.RegistryStr() != host || !strings.HasPrefix(repository, appNamespace) && !strings.HasPrefix(repository, mirrorNamespace) {
		return name.Tag{}, errors.New("image is not in the cluster registry; run podmin push")
	}
	return ref, nil
}

// IsApp reports whether ref identifies repository beneath the cluster app namespace.
func IsApp(ref name.Tag, repository string) bool {
	return ref.RegistryStr() == host && ref.Context().RepositoryStr() == appNamespace+repository
}

// Mirror returns the cluster mirror reference for a source image.
func Mirror(source name.Tag) (name.Tag, error) {
	return tag(fmt.Sprintf("%s/%s%s/%s:%s", host, mirrorNamespace, source.RegistryStr(), source.Context().RepositoryStr(), source.TagStr()))
}

// shorthand reports whether value omits an explicit registry host.
func shorthand(value string) bool {
	first, _, slash := strings.Cut(value, "/")
	return !slash || !strings.Contains(first, ".") && !strings.Contains(first, ":")
}

// tag parses a tagged reference and rejects digests.
func tag(value string) (name.Tag, error) {
	ref, err := name.NewTag(value, name.WeakValidation)
	if err != nil || strings.Contains(value, "@") {
		return name.Tag{}, errors.New("image reference must use a valid tag")
	}
	return ref, nil
}
