// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Index is the single cluster-wide committed desired-state object.
type Index map[string]IndexDeployment

// IndexDeployment references one Pod and its optional Service payload.
type IndexDeployment struct {
	Pod     IndexObject `json:"pod"`
	Service IndexObject `json:"service,omitempty"`
}

// IndexObject is the content-addressed path of an immutable payload.
type IndexObject string

// Digest returns the lowercase SHA-512 digest of body.
func Digest(body []byte) string {
	sum := sha512.Sum512(body)
	return hex.EncodeToString(sum[:])
}

// Verify checks that body is the referenced immutable payload.
func (o IndexObject) Verify(body []byte) error {
	digest, ok := objectDigest(string(o))
	if !ok || Digest(body) != digest {
		return errors.New("SHA-512 mismatch")
	}
	return nil
}

// ParseIndex strictly parses and validates an index.
func ParseIndex(body []byte) (Index, error) {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return Index{}, fmt.Errorf("parse index: %w", err)
	}
	var index Index
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return Index{}, fmt.Errorf("parse index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Index{}, errors.New("index contains invalid trailing JSON")
	}
	if index == nil {
		return Index{}, errors.New("incomplete index")
	}
	services := map[string]bool{}
	for key, deployment := range index {
		nodeGroup, _, ok := DeploymentIdentity(key)
		if !ok || !podObject(string(deployment.Pod), nodeGroup) {
			return Index{}, fmt.Errorf("invalid index deployment %q", key)
		}
		if deployment.Service != "" {
			_, _, identity, ok := ServiceIdentity(string(deployment.Service))
			if !ok || services[identity] {
				return Index{}, fmt.Errorf("invalid or duplicate Service reference for deployment %q", key)
			}
			services[identity] = true
		}
	}
	return index, nil
}

// DeploymentKey returns the unique cluster-wide key for a nodegroup deployment.
func DeploymentKey(nodeGroup, name string) string { return nodeGroup + "/" + name }

// DeploymentIdentity parses a cluster-wide deployment key.
func DeploymentIdentity(key string) (string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 2 || !ValidID(parts[0]) || !ValidID(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ServiceIdentity validates a content-addressed Service key and returns its namespace, name, and combined identity.
func ServiceIdentity(key string) (string, string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 5 || parts[0] != "services" || !ValidNamespace(parts[1]) || !ValidID(parts[2]) || parts[3] != "sha512" {
		return "", "", "", false
	}
	if _, ok := objectDigest(key); !ok {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[1] + "/" + parts[2], true
}

// rejectDuplicateJSONKeys validates one JSON value and rejects duplicate object keys recursively.
func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("index contains invalid trailing JSON")
	}
	return nil
}

// scanJSONValue consumes one JSON value while checking every object's key set.
func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, keyOK := keyToken.(string)
			if !keyOK || seen[key] {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = true
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

// MarshalIndex produces deterministic canonical JSON.
func MarshalIndex(index Index) ([]byte, error) {
	if index == nil {
		index = Index{}
	}
	body, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	if _, err = ParseIndex(body); err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// podObject validates a content-addressed Pod key for nodeGroup.
func podObject(key, nodeGroup string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 5 || parts[0] != "nodegroups" || parts[1] != nodeGroup || parts[2] != "pods" || parts[3] != "sha512" {
		return false
	}
	_, ok := objectDigest(key)
	return ok
}

// objectDigest extracts and validates the SHA-512 digest from a content-addressed YAML key.
func objectDigest(key string) (string, bool) {
	filename := key[strings.LastIndex(key, "/")+1:]
	digest := strings.TrimSuffix(filename, ".yaml")
	decoded, err := hex.DecodeString(digest)
	return digest, strings.HasSuffix(filename, ".yaml") && len(decoded) == sha512.Size && err == nil && digest == strings.ToLower(digest)
}
