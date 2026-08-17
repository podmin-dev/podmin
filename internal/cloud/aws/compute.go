// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Compute provides the EC2 queries required during setup.
type Compute struct{ client *ec2.Client }

// Architecture validates an EC2 instance type and returns its Go architecture.
func (c *Compute) Architecture(ctx context.Context, instanceType string) (string, error) {
	out, err := c.client.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: []types.InstanceType{types.InstanceType(instanceType)}})
	if err != nil {
		return "", err
	}
	if len(out.InstanceTypes) != 1 || out.InstanceTypes[0].ProcessorInfo == nil || out.InstanceTypes[0].NetworkInfo == nil {
		return "", fmt.Errorf("AWS returned incomplete information for %s", instanceType)
	}
	info := out.InstanceTypes[0]
	if info.Hypervisor != types.InstanceTypeHypervisorNitro || !aws.ToBool(info.NetworkInfo.Ipv6Supported) || aws.ToInt32(info.NetworkInfo.Ipv6AddressesPerInterface) < 1 {
		return "", fmt.Errorf("instance type %s must support IPv6 prefix delegation", instanceType)
	}
	if aws.ToInt32(info.NetworkInfo.MaximumNetworkInterfaces) < 2 {
		return "", fmt.Errorf("instance type %s must support at least two network interfaces", instanceType)
	}
	for _, architecture := range info.ProcessorInfo.SupportedArchitectures {
		switch architecture {
		case types.ArchitectureTypeArm64:
			return "arm64", nil
		case types.ArchitectureTypeX8664:
			return "amd64", nil
		}
	}
	return "", fmt.Errorf("instance type %s has no supported amd64 or arm64 architecture", instanceType)
}

// SubnetCIDRs validates a reused VPC, reports its ownership, and allocates stable IPv6 /64s.
func (c *Compute) SubnetCIDRs(ctx context.Context, cluster string, cidr netip.Prefix, nodeGroups []string) (map[string]string, bool, error) {
	vpcs, err := c.client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []types.Filter{{Name: aws.String("cidr-block"), Values: []string{cidr.String()}}}})
	if err != nil {
		return nil, false, err
	}
	if len(vpcs.Vpcs) == 0 {
		return map[string]string{}, true, nil
	}
	if len(vpcs.Vpcs) != 1 {
		return nil, false, fmt.Errorf("multiple VPCs have primary CIDR %s; remove the ambiguity", cidr)
	}
	vpc := vpcs.Vpcs[0]
	vpcID := aws.ToString(vpc.VpcId)
	managed := tagValue(vpc.Tags, "podmin:cluster") == cluster
	for _, attribute := range []types.VpcAttributeName{types.VpcAttributeNameEnableDnsSupport, types.VpcAttributeNameEnableDnsHostnames} {
		out, attributeErr := c.client.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: vpc.VpcId, Attribute: attribute})
		if attributeErr != nil {
			return nil, false, attributeErr
		}
		value := out.EnableDnsSupport
		if attribute == types.VpcAttributeNameEnableDnsHostnames {
			value = out.EnableDnsHostnames
		}
		if value == nil || !aws.ToBool(value.Value) {
			return nil, false, fmt.Errorf("existing VPC %s must enable %s", vpcID, attribute)
		}
	}
	var ipv6 netip.Prefix
	for _, association := range vpc.Ipv6CidrBlockAssociationSet {
		prefix, parseErr := netip.ParsePrefix(aws.ToString(association.Ipv6CidrBlock))
		if parseErr == nil && association.IpSource == types.IpSourceAmazon && prefix.Bits() == 56 {
			if ipv6.IsValid() {
				return nil, false, fmt.Errorf("existing VPC %s has multiple Amazon-provided IPv6 /56 blocks", vpcID)
			}
			ipv6 = prefix.Masked()
		}
	}
	if !ipv6.IsValid() {
		return nil, false, fmt.Errorf("existing VPC %s needs one Amazon-provided IPv6 /56", vpcID)
	}
	if !managed {
		gateways, gatewayErr := c.client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: []types.Filter{{Name: aws.String("attachment.vpc-id"), Values: []string{vpcID}}}})
		if gatewayErr != nil {
			return nil, false, gatewayErr
		}
		if len(gateways.InternetGateways) != 1 {
			return nil, false, fmt.Errorf("existing VPC %s needs exactly one attached internet gateway", vpcID)
		}
	}
	occupied := map[string]bool{}
	owned := map[string]string{}
	var token *string
	for {
		subnets, listErr := c.client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}, NextToken: token})
		if listErr != nil {
			return nil, false, listErr
		}
		for _, subnet := range subnets.Subnets {
			for _, association := range subnet.Ipv6CidrBlockAssociationSet {
				prefix, parseErr := netip.ParsePrefix(aws.ToString(association.Ipv6CidrBlock))
				if parseErr != nil || prefix.Bits() != 64 || !ipv6.Contains(prefix.Addr()) {
					continue
				}
				value := prefix.Masked().String()
				occupied[value] = true
				for _, nodeGroup := range nodeGroups {
					ownedByTags := tagValue(subnet.Tags, "podmin:cluster") == cluster && tagValue(subnet.Tags, "podmin:nodegroup") == nodeGroup
					if ownedByTags || tagValue(subnet.Tags, "Name") == "podmin-"+cluster+"-"+nodeGroup {
						owned[nodeGroup] = value
					}
				}
			}
		}
		if subnets.NextToken == nil {
			break
		}
		token = subnets.NextToken
	}
	result, err := allocateNodeGroupSubnetCIDRs(ipv6, occupied, owned, nodeGroups)
	if err != nil {
		return nil, false, fmt.Errorf("existing VPC %s: %w", vpcID, err)
	}
	return result, managed, nil
}

// allocateNodeGroupSubnetCIDRs retains owned ranges and assigns the first free /64s.
func allocateNodeGroupSubnetCIDRs(ipv6 netip.Prefix, occupied map[string]bool, owned map[string]string, nodeGroups []string) (map[string]string, error) {
	result := map[string]string{}
	names := append([]string(nil), nodeGroups...)
	sort.Strings(names)
	next := 0
	for _, nodeGroup := range names {
		if value := owned[nodeGroup]; value != "" {
			result[nodeGroup] = value
			continue
		}
		for next < 256 {
			address := ipv6.Addr().As16()
			address[7] = byte(next)
			next++
			candidate := netip.PrefixFrom(netip.AddrFrom16(address), 64).String()
			if !occupied[candidate] {
				result[nodeGroup] = candidate
				occupied[candidate] = true
				break
			}
		}
		if result[nodeGroup] == "" {
			return nil, errors.New("insufficient free IPv6 /64 ranges")
		}
	}
	return result, nil
}

// tagValue returns an exact EC2 tag value.
func tagValue(tags []types.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}
