// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

var joolReleases = "https://api.github.com/repos/podplane/jool-builds/releases?per_page=100"
var joolTag = regexp.MustCompile(`^jool-([0-9]+\.[0-9]+\.[0-9]+)-kernel-([0-9][0-9A-Za-z.+~-]*)$`)

// JoolModule is a verified module archive for one exact cloud kernel.
type JoolModule struct {
	Artifact Artifact
	Kernel   string
}

// ResolveJool resolves the newest Jool build for every requested exact kernel release.
func (f *Fetcher) ResolveJool(ctx context.Context, kernels []string) ([]JoolModule, error) {
	if f.Client == nil {
		f.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if f.CacheDir == "" {
		return nil, errors.New("dependency cache directory is required")
	}
	if err := os.MkdirAll(f.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("create dependency cache: %w", err)
	}
	body, err := f.get(ctx, joolReleases, "")
	if err != nil {
		return nil, err
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.Unmarshal(body, &releases); err != nil {
		return nil, err
	}
	type build struct {
		version string
		assets  map[string]string
	}
	builds := map[string]build{}
	for _, release := range releases {
		match := joolTag.FindStringSubmatch(release.Tag)
		if release.Draft || release.Prerelease || match == nil {
			continue
		}
		version := "v" + match[1]
		if current, ok := builds[match[2]]; ok && semver.Compare("v"+current.version, version) >= 0 {
			continue
		}
		assets := make(map[string]string, len(release.Assets))
		for _, asset := range release.Assets {
			assets[asset.Name] = asset.URL
		}
		builds[match[2]] = build{version: match[1], assets: assets}
	}
	if len(builds) == 0 {
		return nil, errors.New("no stable Jool module releases")
	}

	type target struct{ abi, architecture string }
	requested := map[string]target{}
	for _, kernel := range kernels {
		architecture := ""
		for _, candidate := range []string{"amd64", "arm64"} {
			if strings.HasSuffix(kernel, "-cloud-"+candidate) {
				architecture = candidate
				break
			}
		}
		if architecture == "" {
			return nil, fmt.Errorf("unsupported Jool kernel %q", kernel)
		}
		requested[kernel] = target{abi: strings.TrimSuffix(kernel, "-cloud-"+architecture), architecture: architecture}
	}
	kernels = make([]string, 0, len(requested))
	for kernel := range requested {
		kernels = append(kernels, kernel)
	}
	sort.Strings(kernels)
	var result []JoolModule
	checksumsByURL := map[string][]byte{}
	for _, kernel := range kernels {
		target := requested[kernel]
		build, ok := builds[target.abi]
		if !ok {
			return nil, fmt.Errorf("no prebuilt Jool module for kernel %s", kernel)
		}
		checksumsURL := build.assets["SHA256SUMS"]
		if checksumsURL == "" {
			return nil, fmt.Errorf("jool %s kernel %s has no SHA256SUMS", build.version, target.abi)
		}
		checksums := checksumsByURL[checksumsURL]
		if checksums == nil {
			var getErr error
			checksums, getErr = f.get(ctx, checksumsURL, "")
			if getErr != nil {
				return nil, getErr
			}
			checksumsByURL[checksumsURL] = checksums
		}
		filename := fmt.Sprintf("jool-%s-%s-%s.tar.gz", build.version, target.abi, target.architecture)
		url := build.assets[filename]
		if url == "" {
			return nil, fmt.Errorf("jool %s kernel %s has no %s archive", build.version, target.abi, target.architecture)
		}
		expected, checksumErr := checksum(checksums, filename)
		if checksumErr != nil {
			return nil, checksumErr
		}
		key := "jool-" + strings.NewReplacer(".", "-", "+", "-", "~", "-").Replace(target.abi)
		object := fmt.Sprintf("jool-v%s-kernel-%s-linux-%s.tar.gz", build.version, target.abi, target.architecture)
		result = append(result, JoolModule{
			Kernel: kernel,
			Artifact: Artifact{
				Key:          key,
				Name:         "jool.tar.gz",
				Version:      build.version + "-" + target.abi,
				Architecture: target.architecture,
				URL:          url,
				ObjectKey:    "dependencies/jool/" + object,
				Digest:       "sha256:" + expected,
				Path:         filepath.Join(f.CacheDir, "jool", build.version, target.abi, target.architecture, filename),
			},
		})
	}
	return result, nil
}
