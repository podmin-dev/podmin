// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/podmin-dev/podmin/internal/cli/transfer"
	"github.com/podmin-dev/podmin/internal/cli/tui"
)

const maximumResponseSize int64 = 1 << 30

// resolveArtifact resolves one artifact's source and publisher checksum.
func (f Fetcher) resolveArtifact(ctx context.Context, dependency Dependency, version, architecture string) (Artifact, error) {
	if dependency.ReleaseStyle == agentRelease && version == "source" {
		return f.buildAgent(ctx, dependency, architecture)
	}
	upstreamArch := dependency.Architectures[architecture]
	plain := strings.TrimPrefix(version, "v")
	filename := expand(dependency.AssetName, plain, upstreamArch, "", "")
	fileURL := expand(dependency.AssetURL, plain, upstreamArch, filename, "")
	checksumURL := expand(dependency.ChecksumURL, plain, upstreamArch, filename, fileURL)
	checksumName := expand(dependency.ChecksumName, plain, upstreamArch, filename, fileURL)
	checksums, err := f.get(ctx, checksumURL, "")
	if err != nil {
		return Artifact{}, err
	}
	expected, err := checksum(checksums, checksumName)
	if err != nil {
		return Artifact{}, err
	}
	localName := dependency.ObjectName
	dir := filepath.Join(f.CacheDir, dependency.Key, plain, architecture)
	path := filepath.Join(dir, localName)
	objectName := fmt.Sprintf("%s-v%s-linux-%s%s", dependency.Key, plain, architecture, extensions(localName))
	return Artifact{Key: dependency.Key, Name: localName, Version: plain, Architecture: architecture, URL: fileURL, ObjectKey: "dependencies/" + dependency.Key + "/" + objectName, Digest: dependency.ChecksumAlgorithm + ":" + expected, Path: path}, nil
}

// download validates or fetches one resolved artifact.
func (f Fetcher) download(ctx context.Context, artifact Artifact) (Artifact, error) {
	if size, cached := cachedArtifact(artifact.Path, artifact.Digest); cached {
		artifact.Size = size
		if f.Progress != nil {
			f.Progress(tui.Event{Type: tui.Cached, Name: filepath.Base(artifact.URL), Current: size, Total: size})
		}
		return artifact, nil
	}
	body, err := f.get(ctx, artifact.URL, filepath.Base(artifact.URL))
	if err != nil {
		return Artifact{}, err
	}
	algorithm, expected, ok := strings.Cut(artifact.Digest, ":")
	actual, _, digestErr := digest(algorithm, bytes.NewReader(body))
	if !ok || digestErr != nil || actual != expected {
		return Artifact{}, fmt.Errorf("%s digest mismatch", filepath.Base(artifact.URL))
	}
	if err = os.MkdirAll(filepath.Dir(artifact.Path), 0700); err != nil {
		return Artifact{}, err
	}
	if err = atomicFile(artifact.Path, body, 0600); err != nil {
		return Artifact{}, err
	}
	artifact.Size = int64(len(body))
	return artifact, nil
}

// cachedArtifact reports whether a local artifact matches its digest.
func cachedArtifact(path, expected string) (int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	algorithm, value, ok := strings.Cut(expected, ":")
	actual, size, digestErr := digest(algorithm, file)
	closeErr := file.Close()
	return size, ok && digestErr == nil && closeErr == nil && actual == value
}

// expand substitutes catalog URL and filename fields.
func expand(pattern, version, architecture, asset, url string) string {
	return strings.NewReplacer("{version}", version, "{architecture}", architecture, "{asset}", asset, "{url}", url).Replace(pattern)
}

// get retrieves one bounded HTTP response body.
func (f Fetcher) get(ctx context.Context, url, name string) ([]byte, error) {
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
	reader := io.Reader(response.Body)
	if name != "" && f.Progress != nil {
		f.Progress(tui.Event{Type: tui.Started, Name: name, Total: response.ContentLength})
		reader = &transfer.Reader{Reader: response.Body, Name: name, Total: response.ContentLength, Progress: f.Progress}
	}
	body, err := readBounded(reader, maximumResponseSize)
	if name != "" && f.Progress != nil {
		event := tui.Event{Type: tui.Done, Name: name, Current: int64(len(body)), Total: response.ContentLength, Err: err}
		if err != nil {
			event.Type = tui.Failed
		}
		f.Progress(event)
	}
	return body, err
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
