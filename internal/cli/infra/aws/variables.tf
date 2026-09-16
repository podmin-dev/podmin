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
  validation {
    condition     = can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]+$", var.region))
    error_message = "region must be a valid AWS region identifier."
  }
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
variable "manage_vpc" {
  type        = bool
  description = "Whether Podmin owns the VPC lifecycle."
}
variable "nat64" {
  description = "Optional shared NAT64 instance configuration."
  type = object({
    instance_type = string
    architecture  = string
    generation    = string
  })
  default = null
  validation {
    condition = var.nat64 == null ? true : (
      var.nat64.instance_type != "" &&
      contains(["amd64", "arm64"], var.nat64.architecture) &&
      can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$", var.nat64.generation))
    )
    error_message = "nat64 must contain an instance type, supported architecture, and RFC 3339 UTC generation."
  }
}
variable "availability_zones" {
  type        = list(string)
  description = "Available zones used for deterministic NodeGroup placement."
  validation {
    condition     = length(var.availability_zones) > 0
    error_message = "availability_zones must not be empty."
  }
}
variable "nodegroups" {
  description = "Authoritative NodeGroup compute and bootstrap definitions."
  type = map(object({
    size                = number
    instance_type       = string
    architecture        = string
    user_data           = string
    nat64_instance_type = string
    nat64_architecture  = string
  }))
  validation {
    condition = length(var.nodegroups) > 0 && alltrue([
      for name, nodegroup in var.nodegroups :
      can(regex("^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$", name)) &&
      nodegroup.size >= 1 && floor(nodegroup.size) == nodegroup.size &&
      contains(["amd64", "arm64"], nodegroup.architecture) &&
      nodegroup.instance_type != "" && nodegroup.user_data != "" &&
      ((nodegroup.nat64_instance_type == "" && nodegroup.nat64_architecture == "") ||
      (var.nat64 != null && nodegroup.nat64_instance_type != "" && contains(["amd64", "arm64"], nodegroup.nat64_architecture)))
    ])
    error_message = "nodegroups must contain valid names, positive integer sizes, supported architectures, instance types, and user data."
  }
}
variable "nat64_cidrs" {
  type        = map(string)
  description = "Stable public IPv4 /28 allocations for NAT64 subnets."
  default     = {}
}
variable "nat64_ipv6_cidrs" {
  type        = map(string)
  description = "Stable IPv6 /64 allocations for NAT64 subnets."
  default     = {}
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
