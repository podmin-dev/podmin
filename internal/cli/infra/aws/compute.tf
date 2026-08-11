# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0

# Embedded AWS compute resources for the Podmin CLI.

data "aws_ami" "debian" {
  for_each    = toset([for nodegroup in values(var.nodegroups) : nodegroup.architecture])
  most_recent = true
  owners      = ["136693071363"]
  filter {
    name   = "name"
    values = ["debian-13-${each.key == "arm64" ? "arm64" : "amd64"}-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
  filter {
    name   = "architecture"
    values = [each.key == "arm64" ? "arm64" : "x86_64"]
  }
}

resource "aws_security_group" "cluster" {
  name_prefix = "podmin-${var.cluster_id}-"
  vpc_id      = local.vpc_id
  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    ipv6_cidr_blocks = ["::/0"]
  }
}
resource "aws_security_group_rule" "cluster_internal" {
  type                     = "ingress"
  from_port                = 0
  to_port                  = 0
  protocol                 = "-1"
  security_group_id        = aws_security_group.cluster.id
  source_security_group_id = aws_security_group.cluster.id
}

resource "aws_iam_role" "instance" {
  name_prefix        = "podmin-${var.cluster_id}-"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}

removed {
  from = aws_ssm_parameter.identity_ca_key
  lifecycle {
    destroy = false
  }
}

resource "aws_iam_role_policy" "instance" {
  role = aws_iam_role.instance.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    { Effect = "Allow", Action = "s3:ListBucket", Resource = "arn:aws:s3:::${var.bucket}", Condition = { StringLike = { "s3:prefix" = ["dependencies/*", "apps/*", "mirror/*", "deployments/*", "nodegroups/*", "services/*", "dns/*", "identity/*"] } } },
    { Effect = "Allow", Action = "s3:GetObject", Resource = [for prefix in ["dependencies", "apps", "mirror", "deployments", "nodegroups", "services", "dns", "identity"] : "arn:aws:s3:::${var.bucket}/${prefix}/*"] },
    { Effect = "Allow", Action = ["s3:PutObject", "s3:DeleteObject"], Resource = "arn:aws:s3:::${var.bucket}/dns/*" },
    { Effect = "Allow", Action = "s3:PutObject", Resource = "arn:aws:s3:::${var.bucket}/identity/*" },
    { Effect = "Allow", Action = ["ssm:GetParameter", "ssm:GetParameters"], Resource = "arn:aws:ssm:${var.region}:*:parameter/${var.cluster_id}/*" },
    { Effect = "Allow", Action = "secretsmanager:GetSecretValue", Resource = "arn:aws:secretsmanager:${var.region}:*:secret:/${var.cluster_id}/*" },
    { Effect = "Allow", Action = ["ec2:ModifyInstanceAttribute"], Resource = "arn:aws:ec2:${var.region}:*:instance/*", Condition = { StringEquals = { "ec2:ResourceTag/podmin:cluster" = var.cluster_id } } }
  ] })
}
resource "aws_iam_instance_profile" "instance" {
  role = aws_iam_role.instance.name
}
resource "aws_launch_template" "nodegroup" {
  for_each      = var.nodegroups
  name_prefix   = "podmin-${var.cluster_id}-${each.key}-"
  image_id      = data.aws_ami.debian[each.value.architecture].id
  instance_type = each.value.instance_type
  user_data     = each.value.user_data
  iam_instance_profile {
    name = aws_iam_instance_profile.instance.name
  }
  network_interfaces {
    associate_public_ip_address = false
    ipv6_prefix_count           = 1
    security_groups             = [aws_security_group.cluster.id]
  }
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
  }
}
resource "aws_autoscaling_group" "nodegroup" {
  for_each            = var.nodegroups
  desired_capacity    = each.value.size
  min_size            = each.value.size
  max_size            = each.value.size
  vpc_zone_identifier = [aws_subnet.nodegroup[each.key].id]
  launch_template {
    id      = aws_launch_template.nodegroup[each.key].id
    version = aws_launch_template.nodegroup[each.key].latest_version
  }
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 0
    }
  }
  tag {
    key                 = "podmin:cluster"
    value               = var.cluster_id
    propagate_at_launch = true
  }
  tag {
    key                 = "podmin:nodegroup"
    value               = each.key
    propagate_at_launch = true
  }
}
