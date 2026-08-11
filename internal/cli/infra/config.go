// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package infra

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// NodeGroup is an authoritative AWS compute NodeGroup.
type NodeGroup struct {
	Size         int    `json:"size"`
	InstanceType string `json:"instance_type"`
	Architecture string `json:"architecture"`
	UserData     string `json:"user_data"`
}

// Variables are values passed to the module as JSON.
type Variables struct {
	ClusterID   string               `json:"cluster_id"`
	Region      string               `json:"region"`
	Profile     string               `json:"profile"`
	Bucket      string               `json:"bucket"`
	VPCCIDR     string               `json:"vpc_cidr"`
	SubnetCIDRs map[string]string    `json:"subnet_cidrs"`
	NodeGroups  map[string]NodeGroup `json:"nodegroups"`
}

// SelectCommand finds the configured OpenTofu/Terraform executable.
func SelectCommand() (string, error) {
	if override := os.Getenv("PODMIN_TF_CMD"); override != "" {
		path, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("OpenTofu/Terraform command %q: %w", override, err)
		}
		return path, nil
	}
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("OpenTofu/Terraform is required (install tofu or terraform)")
}

// ParseNodeGroup parses NAME[,size=N][,instance-type=TYPE].
func ParseNodeGroup(value string) (string, NodeGroup, error) {
	parts := strings.Split(value, ",")
	nodeGroup := NodeGroup{Size: 1, InstanceType: "t4g.small"}
	if parts[0] == "" {
		return "", nodeGroup, errors.New("NodeGroup name is empty")
	}
	for _, option := range parts[1:] {
		key, val, ok := strings.Cut(option, "=")
		if !ok || val == "" {
			return "", nodeGroup, fmt.Errorf("invalid NodeGroup option %q", option)
		}
		switch key {
		case "size":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return "", nodeGroup, fmt.Errorf("invalid NodeGroup size %q", val)
			}
			nodeGroup.Size = n
		case "instance-type":
			nodeGroup.InstanceType = val
		default:
			return "", nodeGroup, fmt.Errorf("unknown NodeGroup option %q", key)
		}
	}
	return parts[0], nodeGroup, nil
}
