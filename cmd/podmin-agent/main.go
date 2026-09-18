// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"syscall"

	"github.com/podmin-dev/podmin/internal/agent"
	"github.com/podmin-dev/podmin/internal/buildvars"
)

// main parses process settings and runs podmin-agent.
func main() {
	provider := flag.String("provider", "aws", "cloud provider")
	bucket := flag.String("bucket", "", "object storage bucket")
	region := flag.String("region", "", "cloud region")
	cluster := flag.String("cluster", "", "cluster identifier")
	nodeGroup := flag.String("nodegroup", "", "NodeGroup identifier")
	nodeAddress := flag.String("node-address", "", "node IPv6 address")
	ipv6Prefix := flag.String("ipv6-prefix", "", "delegated IPv6 Pod prefix")
	workloadCAPublishBucket := flag.String("workload-ca-publish-bucket", "", "external workload CA publication bucket")
	workloadCAPublishKey := flag.String("workload-ca-publish-key", "", "external workload CA publication object key")
	version := flag.Bool("version", false, "print build version")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "podmin-agent accepts no positional arguments")
		os.Exit(2)
	}
	if *version {
		fmt.Printf("podmin-agent %s\nbuild date: %s\ncommit: %s\ncommit date: %s\nbranch: %s\n", buildvars.BuildVersion(), buildvars.BuildDate(), buildvars.CommitHash(), buildvars.CommitDate(), buildvars.CommitBranch())
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	address, addressErr := netip.ParseAddr(*nodeAddress)
	podPrefix, prefixErr := netip.ParsePrefix(*ipv6Prefix)
	if *provider != "aws" || *bucket == "" || *region == "" || *cluster == "" || *nodeGroup == "" || addressErr != nil || !address.Is6() || address.Is4In6() || !address.IsGlobalUnicast() || prefixErr != nil || !podPrefix.Addr().Is6() || podPrefix.Addr().Is4In6() || !podPrefix.Addr().IsGlobalUnicast() || podPrefix.Bits() != 80 || podPrefix != podPrefix.Masked() || podPrefix.Contains(address) {
		logger.Error("invalid required configuration")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.RunDaemon(ctx, agent.DaemonConfig{Provider: *provider, Bucket: *bucket, Region: *region, Cluster: *cluster, NodeGroup: *nodeGroup, WorkloadCAPublishBucket: *workloadCAPublishBucket, WorkloadCAPublishKey: *workloadCAPublishKey, NodeAddress: address, IPv6Prefix: podPrefix, Logger: logger}); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
