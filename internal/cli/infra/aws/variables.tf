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
    kernel        = string
    generation    = string
    modules = map(object({
      object_key = string
      digest     = string
    }))
  })
  default = null
  validation {
    condition = var.nat64 == null ? true : (
      var.nat64.instance_type != "" &&
      contains(["amd64", "arm64"], var.nat64.architecture) &&
      can(regex("^[0-9][0-9A-Za-z.+~-]*-cloud-(amd64|arm64)$", var.nat64.kernel)) &&
      can(regex("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$", var.nat64.generation)) &&
      length(var.nat64.modules) > 0 && alltrue([
        for kernel, module in var.nat64.modules :
        can(regex("^[0-9][0-9A-Za-z.+~-]*-cloud-(amd64|arm64)$", kernel)) &&
        can(regex("^dependencies/jool/[0-9A-Za-z.+~-]+-linux-(amd64|arm64)\\.tar\\.gz$", module.object_key)) &&
        can(regex("^sha256:[0-9a-f]{64}$", module.digest))
      ])
    )
    error_message = "nat64 must contain an instance type, supported architecture, RFC 3339 UTC generation, and valid Jool modules."
  }
}
variable "images" {
  description = "Resolved immutable Debian images by architecture."
  type = map(object({
    id               = string
    root_device_name = string
  }))
  validation {
    condition = length(var.images) > 0 && alltrue([
      for architecture, image in var.images :
      contains(["amd64", "arm64"], architecture) &&
      can(regex("^ami-[0-9a-f]+$", image.id)) &&
      can(regex("^/dev/[a-z0-9]+$", image.root_device_name))
    ])
    error_message = "images must map supported architectures to immutable AMI IDs and root devices."
  }
}
variable "nodegroups" {
  description = "Authoritative NodeGroup compute and bootstrap definitions."
  type = map(object({
    size                = number
    instance_type       = string
    zone                = string
    architecture        = string
    user_data           = string
    nat64_instance_type = string
    nat64_architecture  = string
    nat64_kernel        = string
  }))
  validation {
    condition = length(var.nodegroups) > 0 && alltrue([
      for name, nodegroup in var.nodegroups :
      can(regex("^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$", name)) &&
      nodegroup.size >= 1 && floor(nodegroup.size) == nodegroup.size &&
      nodegroup.zone != "" &&
      contains(["amd64", "arm64"], nodegroup.architecture) &&
      contains(keys(var.images), nodegroup.architecture) &&
      nodegroup.instance_type != "" && nodegroup.user_data != "" &&
      ((nodegroup.nat64_instance_type == "" && nodegroup.nat64_architecture == "" && nodegroup.nat64_kernel == "") ||
      (var.nat64 != null && nodegroup.nat64_instance_type != "" && contains(["amd64", "arm64"], nodegroup.nat64_architecture) && contains(keys(var.images), nodegroup.nat64_architecture) && can(regex("^[0-9][0-9A-Za-z.+~-]*-cloud-(amd64|arm64)$", nodegroup.nat64_kernel))))
    ])
    error_message = "nodegroups must contain valid names, positive integer sizes, zones, supported architectures, instance types, and user data."
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
