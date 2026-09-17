# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0

# Embedded AWS NAT64 resources for the Podmin CLI.

data "aws_caller_identity" "current" {}

locals {
  nat64_zones = var.nat64 == null ? toset([]) : toset(values(local.nodegroup_az))
  nat64_dedicated = var.nat64 == null ? {} : {
    for name, nodegroup in var.nodegroups : "nodegroup-${name}" => {
      availability_zone = local.nodegroup_az[name]
      instance_type     = nodegroup.nat64_instance_type
      architecture      = nodegroup.nat64_architecture
      nodegroups        = [name]
    } if nodegroup.nat64_instance_type != ""
  }
  nat64_shared = var.nat64 == null ? {} : {
    for zone in local.nat64_zones : "shared-${zone}" => {
      availability_zone = zone
      instance_type     = var.nat64.instance_type
      architecture      = var.nat64.architecture
      nodegroups = [
        for name, nodegroup in var.nodegroups : name
        if local.nodegroup_az[name] == zone && nodegroup.nat64_instance_type == ""
      ]
      } if length([
        for name, nodegroup in var.nodegroups : name
        if local.nodegroup_az[name] == zone && nodegroup.nat64_instance_type == ""
    ]) > 0
  }
  nat64_instances = merge(local.nat64_shared, local.nat64_dedicated)
  nodegroup_nat64 = var.nat64 == null ? {} : {
    for name, nodegroup in var.nodegroups :
    name => nodegroup.nat64_instance_type == "" ? "shared-${local.nodegroup_az[name]}" : "nodegroup-${name}"
  }
}

resource "aws_route" "nat64" {
  for_each                    = var.nat64 == null ? {} : var.nodegroups
  route_table_id              = aws_route_table.nodegroup[each.key].id
  destination_ipv6_cidr_block = "64:ff9b::/96"
  network_interface_id        = aws_network_interface.nat64[local.nodegroup_nat64[each.key]].id
}

resource "aws_subnet" "nat64" {
  for_each                        = local.nat64_zones
  vpc_id                          = local.vpc_id
  availability_zone               = each.key
  cidr_block                      = var.nat64_cidrs[each.key]
  ipv6_cidr_block                 = try(var.nat64_ipv6_cidrs[each.key], cidrsubnet(local.ipv6_cidr, 8, length(var.nodegroups) + index(sort(tolist(local.nat64_zones)), each.key)))
  assign_ipv6_address_on_creation = true
  tags = {
    Name              = "${var.cluster_id}-nat64-${each.key}"
    "podmin:cluster"  = var.cluster_id
    "podmin:nat64-az" = each.key
  }
}
resource "aws_route_table" "nat64" {
  for_each = local.nat64_zones
  vpc_id   = local.vpc_id
  tags     = { Name = "${var.cluster_id}-nat64-${each.key}-routes" }
}
resource "aws_route" "nat64_internet_ipv4" {
  for_each               = local.nat64_zones
  route_table_id         = aws_route_table.nat64[each.key].id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = var.manage_vpc ? aws_internet_gateway.podmin[0].id : data.aws_internet_gateway.existing[0].id
}
resource "aws_route" "nat64_internet_ipv6" {
  for_each                    = local.nat64_zones
  route_table_id              = aws_route_table.nat64[each.key].id
  destination_ipv6_cidr_block = "::/0"
  gateway_id                  = var.manage_vpc ? aws_internet_gateway.podmin[0].id : data.aws_internet_gateway.existing[0].id
}
resource "aws_route_table_association" "nat64" {
  for_each       = aws_subnet.nat64
  subnet_id      = each.value.id
  route_table_id = aws_route_table.nat64[each.key].id
}

resource "aws_security_group" "nat64" {
  for_each    = local.nat64_instances
  name_prefix = "${var.cluster_id}-nat64-${each.key}-"
  vpc_id      = local.vpc_id
  ingress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    ipv6_cidr_blocks = [for name in each.value.nodegroups : aws_subnet.nodegroup[name].ipv6_cidr_block]
  }
  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  tags = {
    Name             = "${var.cluster_id}-nat64-${each.key}"
    "podmin:cluster" = var.cluster_id
    "podmin:nat64"   = each.key
  }
}
resource "aws_security_group" "nat64_management" {
  count       = var.nat64 == null ? 0 : 1
  name_prefix = "${var.cluster_id}-nat64-management-"
  vpc_id      = local.vpc_id
  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  tags = {
    Name             = "${var.cluster_id}-nat64-management"
    "podmin:cluster" = var.cluster_id
  }
}

resource "aws_network_interface" "nat64" {
  for_each           = local.nat64_instances
  subnet_id          = aws_subnet.nat64[each.value.availability_zone].id
  security_groups    = [aws_security_group.nat64[each.key].id]
  ipv6_address_count = 1
  source_dest_check  = false
  tags = {
    Name                = "${var.cluster_id}-nat64-${each.key}"
    "podmin:cluster"    = var.cluster_id
    "podmin:nat64"      = each.key
    "podmin:generation" = var.nat64.generation
  }
}
resource "aws_eip" "nat64" {
  for_each                  = local.nat64_instances
  domain                    = "vpc"
  network_interface         = aws_network_interface.nat64[each.key].id
  associate_with_private_ip = aws_network_interface.nat64[each.key].private_ip
  tags = {
    Name             = "${var.cluster_id}-nat64-${each.key}"
    "podmin:cluster" = var.cluster_id
    "podmin:nat64"   = each.key
  }
}

resource "aws_iam_role" "nat64" {
  for_each           = local.nat64_instances
  name_prefix        = "${var.cluster_id}-nat64-"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy" "nat64" {
  for_each = local.nat64_instances
  role     = aws_iam_role.nat64[each.key].id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = "s3:GetObject", Resource = "arn:aws:s3:::${var.bucket}/dependencies/jool/*" },
    { Effect = "Allow", Action = "ec2:DescribeNetworkInterfaces", Resource = "*" },
    { Effect = "Allow", Action = ["ec2:AttachNetworkInterface", "ec2:DetachNetworkInterface"], Resource = aws_network_interface.nat64[each.key].arn },
    {
      Effect   = "Allow"
      Action   = "ec2:AttachNetworkInterface"
      Resource = "arn:aws:ec2:${var.region}:${data.aws_caller_identity.current.account_id}:instance/*"
      Condition = {
        StringEquals = {
          "ec2:ResourceTag/podmin:cluster" = var.cluster_id
          "ec2:ResourceTag/podmin:nat64"   = each.key
          "ec2:InstanceProfile"            = aws_iam_instance_profile.nat64[each.key].arn
        }
      }
    },
    { Effect = "Allow", Action = "autoscaling:DescribeAutoScalingInstances", Resource = "*" },
    { Effect = "Allow", Action = ["autoscaling:CompleteLifecycleAction", "autoscaling:RecordLifecycleActionHeartbeat"], Resource = "arn:aws:autoscaling:${var.region}:*:autoScalingGroup:*:autoScalingGroupName/${var.cluster_id}-nat64-${each.key}" },
    { Effect = "Allow", Action = ["ssm:DescribeAssociation", "ssm:DescribeDocument", "ssm:GetDeployablePatchSnapshotForInstance", "ssm:GetDocument", "ssm:GetManifest", "ssm:ListAssociations", "ssm:ListInstanceAssociations", "ssm:PutComplianceItems", "ssm:PutConfigurePackageResult", "ssm:PutInventory", "ssm:UpdateAssociationStatus", "ssm:UpdateInstanceAssociationStatus", "ssm:UpdateInstanceInformation"], Resource = "*" },
    { Effect = "Allow", Action = ["ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"], Resource = "*" },
    { Effect = "Allow", Action = ["ec2messages:AcknowledgeMessage", "ec2messages:DeleteMessage", "ec2messages:FailMessage", "ec2messages:GetEndpoint", "ec2messages:GetMessages", "ec2messages:SendReply"], Resource = "*" },
  ] })
}
resource "aws_iam_instance_profile" "nat64" {
  for_each = local.nat64_instances
  role     = aws_iam_role.nat64[each.key].name
}

resource "aws_launch_template" "nat64" {
  for_each      = local.nat64_instances
  name_prefix   = "${var.cluster_id}-nat64-${each.key}-"
  image_id      = var.images[each.value.architecture].id
  instance_type = each.value.instance_type
  user_data = base64gzip(templatefile("${path.module}/nat64.sh.tftpl", {
    architecture      = each.value.architecture
    asg               = "${var.cluster_id}-nat64-${each.key}"
    bucket            = var.bucket
    data_eni          = aws_network_interface.nat64[each.key].id
    data_ipv4         = aws_network_interface.nat64[each.key].private_ip
    data_ipv4_cidr    = aws_subnet.nat64[each.value.availability_zone].cidr_block
    data_ipv4_gateway = cidrhost(aws_subnet.nat64[each.value.availability_zone].cidr_block, 1)
    data_ipv6         = one(aws_network_interface.nat64[each.key].ipv6_addresses)
    data_mac          = aws_network_interface.nat64[each.key].mac_address
    eip               = aws_eip.nat64[each.key].public_ip
    generation        = var.nat64.generation
    hook              = "${var.cluster_id}-nat64-${each.key}-launch"
    jool_modules = join("\n", [for kernel, module in var.nat64.modules :
      "  ${kernel}) jool_object='${module.object_key}'; jool_digest='${module.digest}' ;;"
      if endswith(kernel, "-${each.value.architecture}")
    ])
    region           = var.region
    workload_cidrs   = join(" ", [for name in each.value.nodegroups : aws_subnet.nodegroup[name].ipv6_cidr_block])
    workload_sources = join(", ", [for name in each.value.nodegroups : aws_subnet.nodegroup[name].ipv6_cidr_block])
  }))
  iam_instance_profile {
    name = aws_iam_instance_profile.nat64[each.key].name
  }
  block_device_mappings {
    device_name = var.images[each.value.architecture].root_device_name
    ebs {
      delete_on_termination = true
      encrypted             = true
      volume_type           = "gp3"
    }
  }
  network_interfaces {
    associate_public_ip_address = false
    delete_on_termination       = true
    device_index                = 0
    ipv6_address_count          = 1
    security_groups             = [aws_security_group.nat64_management[0].id]
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_protocol_ipv6          = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  tag_specifications {
    resource_type = "instance"
    tags = {
      Name                = "${var.cluster_id}-nat64-${each.key}"
      "podmin:cluster"    = var.cluster_id
      "podmin:nat64"      = each.key
      "podmin:generation" = var.nat64.generation
    }
  }
}
resource "aws_autoscaling_group" "nat64" {
  for_each                  = local.nat64_instances
  name                      = "${var.cluster_id}-nat64-${each.key}"
  desired_capacity          = 1
  min_size                  = 1
  max_size                  = 2
  wait_for_capacity_timeout = "15m"
  vpc_zone_identifier       = [aws_subnet.nat64[each.value.availability_zone].id]
  launch_template {
    id      = aws_launch_template.nat64[each.key].id
    version = aws_launch_template.nat64[each.key].latest_version
  }
  initial_lifecycle_hook {
    name                 = "${var.cluster_id}-nat64-${each.key}-launch"
    default_result       = "ABANDON"
    heartbeat_timeout    = 900
    lifecycle_transition = "autoscaling:EC2_INSTANCE_LAUNCHING"
  }
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 100
      max_healthy_percentage = 200
      skip_matching          = false
    }
  }
  tag {
    key                 = "Name"
    value               = "${var.cluster_id}-nat64-${each.key}"
    propagate_at_launch = true
  }
  tag {
    key                 = "podmin:cluster"
    value               = var.cluster_id
    propagate_at_launch = true
  }
  tag {
    key                 = "podmin:nat64"
    value               = each.key
    propagate_at_launch = true
  }
  depends_on = [aws_eip.nat64]
}
