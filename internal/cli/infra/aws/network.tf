# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0

# Embedded AWS network resources for the Podmin CLI.

data "aws_vpcs" "exact" {
  filter {
    name   = "cidr-block"
    values = [var.vpc_cidr]
  }
}
data "aws_vpc" "existing" {
  count = length(data.aws_vpcs.exact.ids) == 1 ? 1 : 0
  id    = data.aws_vpcs.exact.ids[0]
}
resource "aws_vpc" "podmin" {
  count                            = length(data.aws_vpcs.exact.ids) == 0 ? 1 : 0
  cidr_block                       = var.vpc_cidr
  assign_generated_ipv6_cidr_block = true
  enable_dns_support               = true
  enable_dns_hostnames             = true
  tags = { Name = var.cluster_id
  }
  lifecycle {
    precondition {
      condition     = length(data.aws_vpcs.exact.ids) <= 1
      error_message = "multiple VPCs have the requested primary CIDR"
    }
  }
}

locals {
  vpc_id = length(data.aws_vpcs.exact.ids) == 1 ? data.aws_vpc.existing[0].id : aws_vpc.podmin[0].id
  existing_ipv6_cidr = length(data.aws_vpcs.exact.ids) == 1 ? try(one([
    for association in data.aws_vpc.existing[0].ipv6_cidr_block_associations : association.ipv6_cidr_block
    if association.ip_source == "amazon"
  ]), "") : ""
  ipv6_cidr = length(data.aws_vpcs.exact.ids) == 1 ? local.existing_ipv6_cidr : aws_vpc.podmin[0].ipv6_cidr_block
}

resource "terraform_data" "vpc_compatibility" {
  input = local.vpc_id
  lifecycle {
    precondition {
      condition     = length(data.aws_vpcs.exact.ids) == 0 || data.aws_vpc.existing[0].enable_dns_support
      error_message = "existing VPC must enable DNS support"
    }
    precondition {
      condition     = length(data.aws_vpcs.exact.ids) == 0 || data.aws_vpc.existing[0].enable_dns_hostnames
      error_message = "existing VPC must enable DNS hostnames"
    }
    precondition {
      condition     = try(split("/", local.ipv6_cidr)[1] == "56", false)
      error_message = "existing VPC needs an Amazon-provided IPv6 /56"
    }
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}
resource "aws_subnet" "nodegroup" {
  for_each                                       = var.nodegroups
  vpc_id                                         = local.vpc_id
  availability_zone                              = data.aws_availability_zones.available.names[index(sort(keys(var.nodegroups)), each.key) % length(data.aws_availability_zones.available.names)]
  ipv6_cidr_block                                = try(var.subnet_cidrs[each.key], cidrsubnet(local.ipv6_cidr, 8, index(sort(keys(var.nodegroups)), each.key)))
  ipv6_native                                    = true
  assign_ipv6_address_on_creation                = true
  enable_resource_name_dns_aaaa_record_on_launch = true
  tags = {
    Name               = "${var.cluster_id}-${each.key}"
    "podmin:cluster"   = var.cluster_id
    "podmin:nodegroup" = each.key
  }
}

resource "aws_route_table" "podmin" {
  vpc_id = local.vpc_id
  tags   = { Name = "${var.cluster_id}-routes" }
}
data "aws_internet_gateway" "existing" {
  count = length(data.aws_vpcs.exact.ids) == 1 ? 1 : 0
  filter {
    name   = "attachment.vpc-id"
    values = [local.vpc_id]
  }
}
resource "aws_internet_gateway" "podmin" {
  count  = length(data.aws_vpcs.exact.ids) == 0 ? 1 : 0
  vpc_id = local.vpc_id
  tags   = { Name = "${var.cluster_id}-internet" }
}
resource "aws_route" "internet_ipv6" {
  route_table_id              = aws_route_table.podmin.id
  destination_ipv6_cidr_block = "::/0"
  gateway_id                  = length(data.aws_vpcs.exact.ids) == 1 ? data.aws_internet_gateway.existing[0].id : aws_internet_gateway.podmin[0].id
}
resource "aws_route_table_association" "nodegroup" {
  for_each       = aws_subnet.nodegroup
  subnet_id      = each.value.id
  route_table_id = aws_route_table.podmin.id
}
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = local.vpc_id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  ip_address_type   = "ipv6"
  route_table_ids   = [aws_route_table.podmin.id]
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Principal = "*", Action = "s3:ListBucket", Resource = "arn:aws:s3:::${var.bucket}", Condition = { StringLike = { "s3:prefix" = ["dependencies/*", "apps/*", "mirror/*", "deployments/*", "nodegroups/*", "services/*", "dns/*", "identity/*"] } } },
    { Effect = "Allow", Principal = "*", Action = "s3:GetObject", Resource = [for prefix in ["dependencies", "apps", "mirror", "deployments", "nodegroups", "services", "dns", "identity"] : "arn:aws:s3:::${var.bucket}/${prefix}/*"] },
    { Effect = "Allow", Principal = "*", Action = ["s3:PutObject", "s3:DeleteObject"], Resource = "arn:aws:s3:::${var.bucket}/dns/*" },
    { Effect = "Allow", Principal = "*", Action = "s3:PutObject", Resource = "arn:aws:s3:::${var.bucket}/identity/*" }
  ] })
  tags = { Name = "${var.cluster_id}-s3" }
}
