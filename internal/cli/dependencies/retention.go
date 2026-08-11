// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

import (
	"sort"
	"time"
)

// Object is retention metadata for one uploaded dependency object.
type Object struct {
	Key, Group, Version string
	Modified            time.Time
}

// Expired selects versions beyond the newest two when all their objects are 28 days old.
func Expired(objects []Object, now time.Time) []Object {
	groups := map[string]map[string][]Object{}
	for _, o := range objects {
		if groups[o.Group] == nil {
			groups[o.Group] = map[string][]Object{}
		}
		groups[o.Group][o.Version] = append(groups[o.Group][o.Version], o)
	}
	var result []Object
	for _, versions := range groups {
		names := make([]string, 0, len(versions))
		for name := range versions {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool { return newest(versions[names[i]]).After(newest(versions[names[j]])) })
		if len(names) <= 2 {
			continue
		}
		for _, name := range names[2:] {
			old := true
			for _, o := range versions[name] {
				if now.Sub(o.Modified) < 28*24*time.Hour {
					old = false
				}
			}
			if old {
				result = append(result, versions[name]...)
			}
		}
	}
	return result
}

// newest returns the latest modification time in a version.
func newest(objects []Object) time.Time {
	var result time.Time
	for _, o := range objects {
		if o.Modified.After(result) {
			result = o.Modified
		}
	}
	return result
}
