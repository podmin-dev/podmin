// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maximumResponseSize int64 = 1 << 30

// fetch downloads one artifact and verifies its publisher checksum.
func (f Fetcher) fetch(ctx context.Context, dependency Dependency, version, architecture string) (Artifact, error) {
	if dependency.ReleaseStyle == agentRelease && version == "source" {
		return f.buildAgent(ctx, dependency, architecture)
	}
	upstreamArch := dependency.Architectures[architecture]
	plain := strings.TrimPrefix(version, "v")
	filename := expand(dependency.AssetName, plain, upstreamArch, "", "")
	fileURL := expand(dependency.AssetURL, plain, upstreamArch, filename, "")
	checksumURL := expand(dependency.ChecksumURL, plain, upstreamArch, filename, fileURL)
	checksumName := expand(dependency.ChecksumName, plain, upstreamArch, filename, fileURL)
	algorithm := dependency.ChecksumAlgorithm
	body, err := f.get(ctx, fileURL)
	if err != nil {
		return Artifact{}, err
	}
	checksums, err := f.get(ctx, checksumURL)
	if err != nil {
		return Artifact{}, err
	}
	expected, err := checksum(checksums, checksumName)
	if err != nil {
		return Artifact{}, err
	}
	actual := digest(algorithm, body)
	if actual != expected {
		return Artifact{}, fmt.Errorf("%s digest mismatch", filename)
	}
	localName := dependency.ObjectName
	if dependency.WrapBinary != "" {
		body, err = tarGzip(dependency.WrapBinary, body)
		if err != nil {
			return Artifact{}, err
		}
		algorithm, actual = "sha512", digest("sha512", body)
	}
	dir := filepath.Join(f.CacheDir, dependency.Key, plain, architecture)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(dir, localName)
	if err = atomicFile(path, body, 0600); err != nil {
		return Artifact{}, err
	}
	objectName := fmt.Sprintf("%s-v%s-linux-%s%s", dependency.Key, plain, architecture, extensions(localName))
	return Artifact{Name: localName, Version: plain, Architecture: architecture, ObjectKey: "dependencies/" + dependency.Key + "/" + objectName, Digest: algorithm + ":" + actual, Path: path}, nil
}

// expand substitutes catalog URL and filename fields.
func expand(pattern, version, architecture, asset, url string) string {
	return strings.NewReplacer("{version}", version, "{architecture}", architecture, "{asset}", asset, "{url}", url).Replace(pattern)
}

// get retrieves one bounded HTTP response body.
func (f Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "podmin")
	response, err := f.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, response.Status)
	}
	if response.ContentLength > maximumResponseSize {
		return nil, fmt.Errorf("GET %s: response exceeds 1 GiB", url)
	}
	return readBounded(response.Body, maximumResponseSize)
}

// readBounded reads at most limit bytes and rejects a body containing more.
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}
