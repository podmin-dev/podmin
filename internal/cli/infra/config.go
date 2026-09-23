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
	Size              int    `json:"size"`
	DiskSize          int    `json:"disk_size"`
	InstanceType      string `json:"instance_type"`
	Zone              string `json:"zone"`
	Architecture      string `json:"architecture"`
	UserData          string `json:"user_data"`
	NAT64InstanceType string `json:"nat64_instance_type"`
	NAT64Architecture string `json:"nat64_architecture"`
	NAT64Kernel       string `json:"nat64_kernel"`
}

// Image identifies an immutable machine image for one architecture.
type Image struct {
	ID             string `json:"id"`
	RootDeviceName string `json:"root_device_name"`
}

// NAT64Module identifies a verified module archive for an exact kernel release.
type NAT64Module struct {
	ObjectKey string `json:"object_key"`
	Digest    string `json:"digest"`
}

// NAT64 configures the shared per-zone instances.
type NAT64 struct {
	InstanceType string                 `json:"instance_type"`
	Architecture string                 `json:"architecture"`
	Kernel       string                 `json:"kernel"`
	Generation   string                 `json:"generation"`
	Modules      map[string]NAT64Module `json:"modules"`
}

// WorkloadCAPublication identifies an external S3 trust-bundle object.
type WorkloadCAPublication struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

// S3Object identifies one externally managed S3 object.
type S3Object struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

// Variables are values passed to the module as JSON.
type Variables struct {
	ClusterID             string                 `json:"cluster_id"`
	Region                string                 `json:"region"`
	Profile               string                 `json:"profile"`
	Bucket                string                 `json:"bucket"`
	WorkloadCAPublication *WorkloadCAPublication `json:"workload_ca_publication"`
	OTelLogsCA            *S3Object              `json:"otel_logs_ca"`
	VPCCIDR               string                 `json:"vpc_cidr"`
	ManageVPC             bool                   `json:"manage_vpc"`
	NAT64                 *NAT64                 `json:"nat64"`
	SubnetCIDRs           map[string]string      `json:"subnet_cidrs"`
	NAT64CIDRs            map[string]string      `json:"nat64_cidrs"`
	NAT64IPv6             map[string]string      `json:"nat64_ipv6_cidrs"`
	Images                map[string]Image       `json:"images"`
	NodeGroups            map[string]NodeGroup   `json:"nodegroups"`
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

// ParseNodeGroup parses NAME[,size=N][,disk-size=GIB][,instance-type=TYPE][,zone=ZONE][,nat64=TYPE].
func ParseNodeGroup(value string) (string, NodeGroup, error) {
	parts := strings.Split(value, ",")
	nodeGroup := NodeGroup{Size: 1, DiskSize: 20, InstanceType: "t4g.small"}
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
		case "disk-size":
			n, err := strconv.Atoi(val)
			if err != nil || n < 8 || n > 16384 {
				return "", nodeGroup, fmt.Errorf("invalid NodeGroup disk size %q (must be 8-16384 GiB)", val)
			}
			nodeGroup.DiskSize = n
		case "instance-type":
			nodeGroup.InstanceType = val
		case "zone":
			nodeGroup.Zone = val
		case "nat64":
			nodeGroup.NAT64InstanceType = val
		default:
			return "", nodeGroup, fmt.Errorf("unknown NodeGroup option %q", key)
		}
	}
	return parts[0], nodeGroup, nil
}

// ParseNAT64 parses the shared NAT64 configuration.
func ParseNAT64(value string) (NAT64, error) {
	key, instanceType, ok := strings.Cut(value, "=")
	if !ok || key != "instance-type" || instanceType == "" || strings.Contains(instanceType, "=") {
		return NAT64{}, errors.New("--nat64 must be instance-type=TYPE")
	}
	return NAT64{InstanceType: instanceType}, nil
}
