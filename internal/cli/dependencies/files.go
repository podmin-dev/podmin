// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checksum extracts one hexadecimal checksum from a publisher sidecar.
func checksum(body []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 1 && len(strings.TrimSpace(string(body))) == len(fields[0]) {
			return strings.ToLower(fields[0]), nil
		}
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == filename {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksum for %s is absent", filename)
}

// digest returns the requested hexadecimal digest and bytes read.
func digest(algorithm string, reader io.Reader) (string, int64, error) {
	hasher := hash.Hash(sha256.New())
	if algorithm == "sha512" {
		hasher = sha512.New()
	}
	size, err := io.Copy(hasher, reader)
	return hex.EncodeToString(hasher.Sum(nil)), size, err
}

// tarGzip wraps a downloaded binary in a deterministic tar.gz archive.
func tarGzip(name string, body []byte) ([]byte, error) {
	var result bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&result, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gzipWriter.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	if _, err := tarWriter.Write(body); err != nil {
		return nil, err
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

// extensions preserves all archive suffixes in an object name.
func extensions(name string) string {
	if strings.HasSuffix(name, ".tar.gz") {
		return ".tar.gz"
	}
	if strings.HasSuffix(name, ".tar.bz2") {
		return ".tar.bz2"
	}
	return filepath.Ext(name)
}

// atomicFile replaces one cache file after syncing its bytes.
func atomicFile(path string, body []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".dependency-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
