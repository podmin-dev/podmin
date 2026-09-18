// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"bytes"
	"compress/gzip"
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
	"golang.org/x/mod/semver"
)

const maximumResponseSize int64 = 1 << 30

// resolveArtifact resolves one artifact's source and publisher checksum.
func (f Fetcher) resolveArtifact(ctx context.Context, dependency Dependency, version, architecture string) (Artifact, error) {
	if dependency.Key == "podmin-agent" && version == "source" {
		return f.buildAgent(ctx, dependency, architecture)
	}
	if dependency.Source == aptRepositorySource {
		return f.resolveAPTPackage(ctx, dependency, architecture)
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

// aptPackageMetadata contains verified fields from one APT package stanza.
type aptPackageMetadata struct {
	Version  string
	Filename string
	Digest   string
}

// resolveAPTPackage resolves one package directly from an APT repository index.
func (f Fetcher) resolveAPTPackage(ctx context.Context, dependency Dependency, architecture string) (Artifact, error) {
	upstreamArch := dependency.Architectures[architecture]
	indexURL := expand(dependency.PackageIndex, "", upstreamArch, "", "")
	index, err := f.get(ctx, indexURL, "")
	if err != nil {
		return Artifact{}, err
	}
	if strings.HasSuffix(indexURL, ".gz") {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(index))
		if gzipErr != nil {
			return Artifact{}, fmt.Errorf("decompress %s package index: %w", dependency.Key, gzipErr)
		}
		index, err = readBounded(reader, maximumResponseSize)
		closeErr := reader.Close()
		if err != nil {
			return Artifact{}, err
		}
		if closeErr != nil {
			return Artifact{}, closeErr
		}
	}
	packages, err := aptPackages(index, dependency.Key, upstreamArch, dependency.ChecksumAlgorithm)
	if err != nil {
		return Artifact{}, err
	}
	var selected aptPackageMetadata
	if dependency.Major == 0 {
		if len(packages) != 1 {
			return Artifact{}, fmt.Errorf("package index contains %d versions of unconstrained package %s for %s", len(packages), dependency.Key, upstreamArch)
		}
		selected = packages[0]
	} else {
		best := ""
		for _, candidate := range packages {
			version := "v" + candidate.Version
			if !semver.IsValid(version) || semver.Prerelease(version) != "" || semver.Major(version) != fmt.Sprintf("v%d", dependency.Major) || dependency.Minor > 0 && semver.MajorMinor(version) != fmt.Sprintf("v%d.%d", dependency.Major, dependency.Minor) {
				continue
			}
			if semver.Compare(version, best) > 0 {
				best = version
				selected = candidate
			}
		}
		if best == "" {
			return Artifact{}, fmt.Errorf("no stable constrained package for %s on %s", dependency.Key, upstreamArch)
		}
	}
	fileURL := expand(dependency.AssetURL, selected.Version, upstreamArch, selected.Filename, "")
	localName := dependency.ObjectName
	path := filepath.Join(f.CacheDir, dependency.Key, selected.Version, architecture, localName)
	objectName := fmt.Sprintf("%s-v%s-linux-%s%s", dependency.Key, selected.Version, architecture, extensions(localName))
	return Artifact{Key: dependency.Key, Name: localName, Version: selected.Version, Architecture: architecture, URL: fileURL, ObjectKey: "dependencies/" + dependency.Key + "/" + objectName, Digest: dependency.ChecksumAlgorithm + ":" + selected.Digest, Path: path}, nil
}

// aptPackages returns validated package metadata matching a name, architecture, and digest algorithm.
func aptPackages(index []byte, name, architecture, algorithm string) ([]aptPackageMetadata, error) {
	digestField := "SHA512"
	digestLength := 128
	if algorithm == "sha256" {
		digestField = "SHA256"
		digestLength = 64
	} else if algorithm != "sha512" {
		return nil, fmt.Errorf("unsupported package digest algorithm %q", algorithm)
	}
	var packages []aptPackageMetadata
	for _, stanza := range strings.Split(strings.ReplaceAll(string(index), "\r\n", "\n"), "\n\n") {
		fields := make(map[string]string)
		for _, line := range strings.Split(stanza, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if ok {
				fields[key] = value
			}
		}
		if fields["Package"] != name || fields["Architecture"] != architecture {
			continue
		}
		digest := strings.ToLower(fields[digestField])
		filename := fields["Filename"]
		if fields["Version"] == "" || !strings.HasPrefix(filename, "pool/") || filepath.Clean(filename) != filename || len(digest) != digestLength || strings.Trim(digest, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("invalid package metadata for %s %s %s", name, fields["Version"], architecture)
		}
		packages = append(packages, aptPackageMetadata{Version: fields["Version"], Filename: filename, Digest: digest})
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("package %s for %s is missing from publisher index", name, architecture)
	}
	return packages, nil
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
