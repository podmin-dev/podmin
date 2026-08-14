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
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Resolve returns the greatest stable release satisfying the version constraint.
func Resolve(ctx context.Context, client *http.Client, dependency Dependency) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, dependency.Releases, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "podmin")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release API returned %s", response.Status)
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.NewDecoder(response.Body).Decode(&releases); err != nil {
		return "", err
	}
	best := ""
	for _, release := range releases {
		version := release.Tag
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		if release.Prerelease || !semver.IsValid(version) || semver.Prerelease(version) != "" {
			continue
		}
		if dependency.Major > 0 && semver.Major(version) != fmt.Sprintf("v%d", dependency.Major) {
			continue
		}
		if dependency.Minor > 0 && semver.MajorMinor(version) != fmt.Sprintf("v%d.%d", dependency.Major, dependency.Minor) {
			continue
		}
		if semver.Compare(version, best) > 0 {
			best = version
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable constrained release for %s", dependency.Key)
	}
	return best, nil
}

// resolveDatedRelease returns the newest stable date-based release tag.
func (f Fetcher) resolveDatedRelease(ctx context.Context, endpoint string) (string, error) {
	body, err := f.get(ctx, endpoint, "")
	if err != nil {
		return "", err
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Name       string `json:"name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(body, &releases); err != nil {
		return "", err
	}
	var versions []string
	for _, release := range releases {
		tag := release.Tag
		if tag == "" {
			tag = release.Name
		}
		version := strings.TrimPrefix(tag, "release-")
		if !release.Prerelease && len(version) >= 8 && version[0] >= '0' && version[0] <= '9' {
			versions = append(versions, version)
		}
	}
	sort.Strings(versions)
	if len(versions) == 0 {
		return "", errors.New("no stable gVisor release")
	}
	return versions[len(versions)-1], nil
}
