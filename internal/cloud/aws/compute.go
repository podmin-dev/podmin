// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/podmin-dev/podmin/internal/cloud"
)

const debianImageOwner = "136693071363"

var debianImageName = regexp.MustCompile(`^debian-13-(amd64|arm64)-([0-9]{8}-[0-9]+)$`)

// Compute provides the EC2 queries required during setup.
type Compute struct {
	client      *ec2.Client
	autoscaling *autoscaling.Client
	http        *http.Client
}

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

// Image resolves the latest official Debian 13 AMI and its exact initial kernel release.
func (c *Compute) Image(ctx context.Context, architecture string) (cloud.Image, error) {
	awsArchitecture := architecture
	if architecture == "amd64" {
		awsArchitecture = "x86_64"
	} else if architecture != "arm64" {
		return cloud.Image{}, fmt.Errorf("unsupported image architecture %q", architecture)
	}
	input := &ec2.DescribeImagesInput{
		Owners: []string{debianImageOwner},
		Filters: []types.Filter{
			{Name: aws.String("architecture"), Values: []string{awsArchitecture}},
			{Name: aws.String("name"), Values: []string{"debian-13-" + architecture + "-*"}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("virtualization-type"), Values: []string{"hvm"}},
		},
	}
	var images []types.Image
	paginator := ec2.NewDescribeImagesPaginator(c.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.Image{}, err
		}
		images = append(images, page.Images...)
	}
	sort.Slice(images, func(i, j int) bool {
		return aws.ToString(images[i].CreationDate) > aws.ToString(images[j].CreationDate)
	})
	if len(images) == 0 {
		return cloud.Image{}, fmt.Errorf("AWS returned no official Debian 13 %s image", architecture)
	}
	image := images[0]
	match := debianImageName.FindStringSubmatch(aws.ToString(image.Name))
	if match == nil || match[1] != architecture || image.Architecture != types.ArchitectureValues(awsArchitecture) {
		return cloud.Image{}, fmt.Errorf("AWS returned invalid Debian image %q", aws.ToString(image.Name))
	}
	httpClient := c.http
	if httpClient == nil {
		httpClient = &http.Client{Timeout: time.Minute}
	}
	url := fmt.Sprintf("https://cloud.debian.org/images/cloud/trixie/%s/debian-13-ec2-%s-%s.json", match[2], architecture, match[2])
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return cloud.Image{}, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return cloud.Image{}, fmt.Errorf("read Debian image manifest: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return cloud.Image{}, fmt.Errorf("read Debian image manifest: %s", response.Status)
	}
	kernel, err := debianKernel(io.LimitReader(response.Body, 16<<20), architecture, match[2])
	if err != nil {
		return cloud.Image{}, err
	}
	result := cloud.Image{ID: aws.ToString(image.ImageId), Kernel: kernel, RootDeviceName: aws.ToString(image.RootDeviceName)}
	if result.ID == "" || result.RootDeviceName == "" {
		return cloud.Image{}, fmt.Errorf("AWS returned incomplete information for Debian image %q", aws.ToString(image.Name))
	}
	return result, nil
}

// debianKernel reads the exact cloud kernel from an official Debian image manifest.
func debianKernel(reader io.Reader, architecture, build string) (string, error) {
	var manifest struct {
		Items []struct {
			Kind string `json:"kind"`
			Data struct {
				Info struct {
					Architecture string `json:"arch"`
					Release      string `json:"release"`
					ReleaseID    string `json:"release_id"`
					Vendor       string `json:"vendor"`
					Version      string `json:"version"`
				} `json:"info"`
				Packages []struct {
					Name string `json:"name"`
				} `json:"packages"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		return "", fmt.Errorf("decode Debian image manifest: %w", err)
	}
	prefix := "linux-image-"
	suffix := "-cloud-" + architecture
	var kernels []string
	for _, item := range manifest.Items {
		info := item.Data.Info
		if item.Kind != "Build" || info.Architecture != architecture || info.Release != "trixie" || info.ReleaseID != "13" || info.Vendor != "ec2" || info.Version != build {
			continue
		}
		for _, pkg := range item.Data.Packages {
			kernel := strings.TrimPrefix(pkg.Name, prefix)
			if kernel != pkg.Name && len(kernel) > 0 && kernel[0] >= '0' && kernel[0] <= '9' && strings.HasSuffix(kernel, suffix) {
				kernels = append(kernels, kernel)
			}
		}
	}
	if len(kernels) != 1 {
		return "", fmt.Errorf("debian image %s/%s contains %d concrete cloud kernels", build, architecture, len(kernels))
	}
	return kernels[0], nil
}

// Network validates a reused VPC and allocates stable workload and NAT64 subnet CIDRs.
func (c *Compute) Network(ctx context.Context, cluster string, cidr netip.Prefix, requestedZones map[string]string, nat64 bool) (cloud.Network, error) {
	zones, err := c.availabilityZones(ctx)
	if err != nil {
		return cloud.Network{}, err
	}
	nodeGroupZones, usedZones, err := resolveNodeGroupZones(requestedZones, zones)
	if err != nil {
		return cloud.Network{}, err
	}
	nodeGroups := make([]string, 0, len(requestedZones))
	for name := range requestedZones {
		nodeGroups = append(nodeGroups, name)
	}
	sort.Strings(nodeGroups)
	network := cloud.Network{NodeGroupZones: nodeGroupZones, NodeGroupCIDRs: map[string]string{}, NAT64CIDRs: map[string]string{}, NAT64IPv6CIDRs: map[string]string{}}
	nat64Zones := []string{}
	if nat64 {
		nat64Zones = usedZones
		if len(nodeGroups)+len(nat64Zones) > 256 {
			return cloud.Network{}, errors.New("NodeGroup and NAT64 subnets exceed the Amazon-provided IPv6 /56")
		}
	}
	vpcs, err := c.client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []types.Filter{{Name: aws.String("cidr-block"), Values: []string{cidr.String()}}}})
	if err != nil {
		return cloud.Network{}, err
	}
	if len(vpcs.Vpcs) == 0 {
		network.ManageVPC = true
		if nat64 {
			network.NAT64CIDRs, err = allocateNAT64SubnetCIDRs(cidr, nil, nil, nat64Zones)
		}
		return network, err
	}
	if len(vpcs.Vpcs) != 1 {
		return cloud.Network{}, fmt.Errorf("multiple VPCs have primary CIDR %s; remove the ambiguity", cidr)
	}
	vpc := vpcs.Vpcs[0]
	vpcID := aws.ToString(vpc.VpcId)
	managed := tagValue(vpc.Tags, "podmin:cluster") == cluster
	network.ManageVPC = managed
	for _, attribute := range []types.VpcAttributeName{types.VpcAttributeNameEnableDnsSupport, types.VpcAttributeNameEnableDnsHostnames} {
		out, attributeErr := c.client.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: vpc.VpcId, Attribute: attribute})
		if attributeErr != nil {
			return cloud.Network{}, attributeErr
		}
		value := out.EnableDnsSupport
		if attribute == types.VpcAttributeNameEnableDnsHostnames {
			value = out.EnableDnsHostnames
		}
		if value == nil || !aws.ToBool(value.Value) {
			return cloud.Network{}, fmt.Errorf("existing VPC %s must enable %s", vpcID, attribute)
		}
	}
	var ipv6 netip.Prefix
	for _, association := range vpc.Ipv6CidrBlockAssociationSet {
		prefix, parseErr := netip.ParsePrefix(aws.ToString(association.Ipv6CidrBlock))
		if parseErr == nil && association.IpSource == types.IpSourceAmazon && prefix.Bits() == 56 {
			if ipv6.IsValid() {
				return cloud.Network{}, fmt.Errorf("existing VPC %s has multiple Amazon-provided IPv6 /56 blocks", vpcID)
			}
			ipv6 = prefix.Masked()
		}
	}
	if !ipv6.IsValid() {
		return cloud.Network{}, fmt.Errorf("existing VPC %s needs one Amazon-provided IPv6 /56", vpcID)
	}
	if !managed {
		gateways, gatewayErr := c.client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{Filters: []types.Filter{{Name: aws.String("attachment.vpc-id"), Values: []string{vpcID}}}})
		if gatewayErr != nil {
			return cloud.Network{}, gatewayErr
		}
		if len(gateways.InternetGateways) != 1 {
			return cloud.Network{}, fmt.Errorf("existing VPC %s needs exactly one attached internet gateway", vpcID)
		}
	}
	occupiedIPv6 := map[string]bool{}
	ownedIPv6 := map[string]string{}
	ownedNAT64IPv6 := map[string]string{}
	occupiedIPv4 := []netip.Prefix{}
	ownedIPv4 := map[string]string{}
	var token *string
	for {
		subnets, listErr := c.client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}, NextToken: token})
		if listErr != nil {
			return cloud.Network{}, listErr
		}
		for _, subnet := range subnets.Subnets {
			if prefix, parseErr := netip.ParsePrefix(aws.ToString(subnet.CidrBlock)); parseErr == nil {
				prefix = prefix.Masked()
				occupiedIPv4 = append(occupiedIPv4, prefix)
				zone := tagValue(subnet.Tags, "podmin:nat64-az")
				if tagValue(subnet.Tags, "podmin:cluster") == cluster && zone != "" {
					ownedIPv4[zone] = prefix.String()
				}
			}
			for _, association := range subnet.Ipv6CidrBlockAssociationSet {
				prefix, parseErr := netip.ParsePrefix(aws.ToString(association.Ipv6CidrBlock))
				if parseErr != nil || prefix.Bits() != 64 || !ipv6.Contains(prefix.Addr()) {
					continue
				}
				value := prefix.Masked().String()
				occupiedIPv6[value] = true
				if zone := tagValue(subnet.Tags, "podmin:nat64-az"); tagValue(subnet.Tags, "podmin:cluster") == cluster && zone != "" {
					ownedNAT64IPv6[zone] = value
				}
				for _, nodeGroup := range nodeGroups {
					ownedByTags := tagValue(subnet.Tags, "podmin:cluster") == cluster && tagValue(subnet.Tags, "podmin:nodegroup") == nodeGroup
					if ownedByTags || tagValue(subnet.Tags, "Name") == "podmin-"+cluster+"-"+nodeGroup {
						ownedIPv6[nodeGroup] = value
					}
				}
			}
		}
		if subnets.NextToken == nil {
			break
		}
		token = subnets.NextToken
	}
	allocationNames := append([]string(nil), nodeGroups...)
	for _, zone := range nat64Zones {
		name := "nat64:" + zone
		allocationNames = append(allocationNames, name)
		ownedIPv6[name] = ownedNAT64IPv6[zone]
	}
	allocations, err := allocateNodeGroupSubnetCIDRs(ipv6, occupiedIPv6, ownedIPv6, allocationNames)
	if err != nil {
		return cloud.Network{}, fmt.Errorf("existing VPC %s: %w", vpcID, err)
	}
	for _, name := range nodeGroups {
		network.NodeGroupCIDRs[name] = allocations[name]
	}
	for _, zone := range nat64Zones {
		network.NAT64IPv6CIDRs[zone] = allocations["nat64:"+zone]
	}
	if nat64 {
		network.NAT64CIDRs, err = allocateNAT64SubnetCIDRs(cidr, occupiedIPv4, ownedIPv4, nat64Zones)
		if err != nil {
			return cloud.Network{}, fmt.Errorf("existing VPC %s: %w", vpcID, err)
		}
	}
	return network, nil
}

// availabilityZones returns stable available Availability Zone names for this region.
func (c *Compute) availabilityZones(ctx context.Context) ([]string, error) {
	out, err := c.client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{Filters: []types.Filter{{Name: aws.String("state"), Values: []string{"available"}}}})
	if err != nil {
		return nil, err
	}
	zones := make([]string, 0, len(out.AvailabilityZones))
	for _, zone := range out.AvailabilityZones {
		if name := aws.ToString(zone.ZoneName); name != "" && aws.ToString(zone.ZoneType) == "availability-zone" {
			zones = append(zones, name)
		}
	}
	sort.Strings(zones)
	if len(zones) == 0 {
		return nil, errors.New("AWS returned no available Availability Zones")
	}
	return zones, nil
}

// resolveNodeGroupZones validates explicit zones and defaults omitted zones to the first available zone.
func resolveNodeGroupZones(requested map[string]string, available []string) (map[string]string, []string, error) {
	if len(available) == 0 {
		return nil, nil, errors.New("AWS returned no available Availability Zones")
	}
	valid := make(map[string]bool, len(available))
	for _, zone := range available {
		valid[zone] = true
	}
	resolved := make(map[string]string, len(requested))
	seen := map[string]bool{}
	var used []string
	for name, value := range requested {
		zone := value
		if zone == "" {
			zone = available[0]
		} else if len(zone) == 1 && zone[0] >= 'a' && zone[0] <= 'z' {
			for _, candidate := range available {
				if strings.HasSuffix(candidate, zone) {
					zone = candidate
					break
				}
			}
		}
		if !valid[zone] {
			return nil, nil, fmt.Errorf("NodeGroup %q zone %q is unavailable; choose one of %s", name, value, strings.Join(available, ", "))
		}
		resolved[name] = zone
		if !seen[zone] {
			seen[zone] = true
			used = append(used, zone)
		}
	}
	sort.Strings(used)
	return resolved, used, nil
}

// allocateNAT64SubnetCIDRs retains owned /28s and assigns free ranges from the end of the VPC CIDR.
func allocateNAT64SubnetCIDRs(vpc netip.Prefix, occupied []netip.Prefix, owned map[string]string, zones []string) (map[string]string, error) {
	if !vpc.Addr().Is4() || vpc.Bits() > 28 {
		return nil, errors.New("VPC CIDR must contain one IPv4 /28 per NAT64 Availability Zone")
	}
	result := map[string]string{}
	names := append([]string(nil), zones...)
	sort.Strings(names)
	for _, zone := range names {
		if value := owned[zone]; value != "" {
			prefix, err := netip.ParsePrefix(value)
			if err != nil || prefix.Bits() != 28 || !vpc.Contains(prefix.Addr()) {
				return nil, fmt.Errorf("owned NAT64 subnet for %s is not a /28 within the VPC", zone)
			}
			result[zone] = prefix.Masked().String()
			occupied = append(occupied, prefix.Masked())
		}
	}
	baseBytes := vpc.Masked().Addr().As4()
	base := binary.BigEndian.Uint32(baseBytes[:])
	count := uint32(1) << uint32(28-vpc.Bits())
	for _, zone := range names {
		if result[zone] != "" {
			continue
		}
		for index := count; index > 0; index-- {
			value := make([]byte, 4)
			binary.BigEndian.PutUint32(value, base+(index-1)*16)
			candidate := netip.PrefixFrom(netip.AddrFrom4([4]byte(value)), 28)
			overlaps := false
			for _, prefix := range occupied {
				if candidate.Contains(prefix.Addr()) || prefix.Contains(candidate.Addr()) {
					overlaps = true
					break
				}
			}
			if overlaps {
				continue
			}
			result[zone] = candidate.String()
			occupied = append(occupied, candidate)
			break
		}
		if result[zone] == "" {
			return nil, errors.New("insufficient free IPv4 /28 ranges for NAT64 subnets")
		}
	}
	return result, nil
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
