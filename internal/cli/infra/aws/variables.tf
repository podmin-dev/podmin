# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0

# Inputs for the Podmin CLI's embedded AWS module.

variable "cluster_id" {
  type        = string
  description = "Podmin cluster identifier."
  validation {
    condition     = can(regex("^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$", var.cluster_id))
    error_message = "cluster_id must be a valid Podmin identifier."
  }
}
variable "region" {
  type        = string
  description = "AWS region containing the cluster."
}
variable "profile" {
  type        = string
  description = "Optional shared AWS configuration profile."
  default     = ""
}
variable "bucket" {
  type        = string
  description = "Private cluster object storage bucket."
}
variable "vpc_cidr" {
  type        = string
  description = "Primary private IPv4 CIDR used to find or create the VPC."
  validation {
    condition     = can(cidrhost(var.vpc_cidr, 0)) && !can(regex(":", var.vpc_cidr))
    error_message = "vpc_cidr must be an IPv4 CIDR."
  }
}
variable "nodegroups" {
  description = "Authoritative NodeGroup compute and bootstrap definitions."
  type = map(object({
    size          = number
    instance_type = string
    architecture  = string
    user_data     = string
  }))
  validation {
    condition = length(var.nodegroups) > 0 && alltrue([
      for name, nodegroup in var.nodegroups :
      can(regex("^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$", name)) &&
      nodegroup.size >= 1 && floor(nodegroup.size) == nodegroup.size &&
      contains(["amd64", "arm64"], nodegroup.architecture) &&
      nodegroup.instance_type != "" && nodegroup.user_data != ""
    ])
    error_message = "nodegroups must contain valid names, positive integer sizes, supported architectures, instance types, and user data."
  }
}
variable "subnet_cidrs" {
  type        = map(string)
  description = "Stable IPv6 /64 allocations when reusing an existing VPC."
  default     = {}
  validation {
    condition = alltrue([
      for name, cidr in var.subnet_cidrs :
      contains(keys(var.nodegroups), name) && can(cidrhost(cidr, 0)) && can(regex(":", cidr)) && can(regex("/64$", cidr))
    ])
    error_message = "subnet_cidrs must map configured NodeGroups to IPv6 /64 CIDRs."
  }
}
