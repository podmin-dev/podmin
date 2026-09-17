// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

const nat64ReadyTimeout = 20 * time.Minute

// WaitNAT64 waits for each NAT64 refresh and its stable ENI handoff to finish.
func (c *Compute) WaitNAT64(ctx context.Context, cluster string, groups []string, generation time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, nat64ReadyTimeout)
	defer cancel()
	for _, group := range groups {
		if err := c.waitNAT64(ctx, cluster, group, generation); err != nil {
			return err
		}
	}
	return nil
}

// waitNAT64 waits for one Auto Scaling Group to complete the current setup refresh.
func (c *Compute) waitNAT64(ctx context.Context, cluster, group string, generation time.Time) error {
	name := cluster + "-nat64-" + group
	var refreshID string
	var lastStatus, lastReason string
	for {
		refreshes, err := c.autoscaling.DescribeInstanceRefreshes(ctx, &autoscaling.DescribeInstanceRefreshesInput{AutoScalingGroupName: &name, MaxRecords: aws.Int32(20)})
		if err != nil {
			return fmt.Errorf("inspect NAT64 refresh for %s: %w", name, err)
		}
		if refresh := currentNAT64Refresh(refreshes.InstanceRefreshes, refreshID, generation); refresh != nil {
			refreshID = aws.ToString(refresh.InstanceRefreshId)
			lastStatus = string(refresh.Status)
			lastReason = aws.ToString(refresh.StatusReason)
			switch status := string(refresh.Status); status {
			case "Successful":
				ready, readyErr := c.nat64Ready(ctx, name, cluster, group, generation.Format(time.RFC3339Nano))
				if readyErr != nil {
					return readyErr
				}
				if ready {
					return nil
				}
			case "Failed", "Cancelled", "RollbackFailed", "RollbackSuccessful":
				return c.nat64RefreshError(ctx, name, refreshID, status, aws.ToString(refresh.StatusReason))
			}
		}
		select {
		case <-ctx.Done():
			diagnosticCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return c.nat64RefreshError(diagnosticCtx, name, refreshID, "timed out while "+lastStatus, lastReason)
		case <-time.After(5 * time.Second):
		}
	}
}

// currentNAT64Refresh returns the selected refresh or the newest refresh from this setup.
func currentNAT64Refresh(refreshes []autoscalingtypes.InstanceRefresh, refreshID string, generation time.Time) *autoscalingtypes.InstanceRefresh {
	var newest *autoscalingtypes.InstanceRefresh
	for i := range refreshes {
		refresh := &refreshes[i]
		if refreshID != "" {
			if aws.ToString(refresh.InstanceRefreshId) == refreshID {
				return refresh
			}
			continue
		}
		if refresh.StartTime != nil && !refresh.StartTime.Before(generation) && (newest == nil || refresh.StartTime.After(*newest.StartTime)) {
			newest = refresh
		}
	}
	return newest
}

// nat64Ready verifies that the refreshed instance owns the stable ENI.
func (c *Compute) nat64Ready(ctx context.Context, name, cluster, group, generation string) (bool, error) {
	groups, err := c.autoscaling.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{name}})
	if err != nil {
		return false, fmt.Errorf("inspect NAT64 group %s: %w", name, err)
	}
	if len(groups.AutoScalingGroups) != 1 || groups.AutoScalingGroups[0].LaunchTemplate == nil {
		return false, fmt.Errorf("AWS returned incomplete NAT64 group %s", name)
	}
	autoScalingGroup := groups.AutoScalingGroups[0]
	instanceID := ""
	for _, instance := range autoScalingGroup.Instances {
		if string(instance.LifecycleState) == "InService" && instance.LaunchTemplate != nil && aws.ToString(instance.LaunchTemplate.Version) == aws.ToString(autoScalingGroup.LaunchTemplate.Version) {
			if instanceID != "" {
				return false, nil
			}
			instanceID = aws.ToString(instance.InstanceId)
		}
	}
	if instanceID == "" {
		return false, nil
	}
	instances, err := c.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return false, fmt.Errorf("inspect NAT64 instance %s: %w", instanceID, err)
	}
	if len(instances.Reservations) != 1 || len(instances.Reservations[0].Instances) != 1 || tagValue(instances.Reservations[0].Instances[0].Tags, "podmin:generation") != generation {
		return false, nil
	}
	interfaces, err := c.client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{Filters: []types.Filter{
		{Name: aws.String("tag:podmin:cluster"), Values: []string{cluster}},
		{Name: aws.String("tag:podmin:nat64"), Values: []string{group}},
	}})
	if err != nil {
		return false, fmt.Errorf("inspect stable NAT64 interface for %s: %w", name, err)
	}
	return len(interfaces.NetworkInterfaces) == 1 && interfaces.NetworkInterfaces[0].Attachment != nil && aws.ToString(interfaces.NetworkInterfaces[0].Attachment.InstanceId) == instanceID, nil
}

// nat64RefreshError includes recent scaling activity in a failed setup result.
func (c *Compute) nat64RefreshError(ctx context.Context, name, refreshID, status, reason string) error {
	activities, err := c.autoscaling.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{AutoScalingGroupName: &name, MaxRecords: aws.Int32(5)})
	if err != nil {
		return fmt.Errorf("NAT64 refresh %s for %s %s: %s; inspect scaling activities: %v", refreshID, name, status, reason, err)
	}
	details := make([]string, 0, len(activities.Activities))
	for _, activity := range activities.Activities {
		detail := aws.ToString(activity.Description)
		if message := aws.ToString(activity.StatusMessage); message != "" {
			detail += ": " + message
		}
		details = append(details, detail)
	}
	return fmt.Errorf("NAT64 refresh %s for %s %s: %s; recent activity: %s", refreshID, name, status, reason, strings.Join(details, "; "))
}
