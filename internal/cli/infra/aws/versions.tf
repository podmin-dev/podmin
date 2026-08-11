# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0

# Version requirements for the Podmin CLI's embedded AWS module.

terraform {
  required_version = ">= 1.11"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
  backend "s3" {}
}

provider "aws" {
  region  = var.region
  profile = var.profile == "" ? null : var.profile
}
