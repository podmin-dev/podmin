# Infrastructure and Agent

## Setup

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --space default \
  --space workers,size=3,instance-type=c8g.large
```

Setup:

- Fetches the latest dependencies to the local cache.
- Resolves every Space's CPU architecture and uploads matching dependencies.
- Ensures required Pod sandbox images are available in the cluster image store.
- Reuses the VPC whose primary IPv4 CIDR exactly matches `--vpc-cidr`, or creates one; incompatible or ambiguous matches fail.
- Applies OpenTofu/Terraform with state stored in the cluster bucket.
- Creates a VPC, public IPv6 subnets, route tables, security groups, IAM roles, and one Auto Scaling Group per Space.
- Embeds cloud-init user-data with pinned dependency versions.
- Rolling-rotates VMs when dependencies or user-data change.

Public subnets provide direct IPv6 egress without NAT. Security groups expose no ports to the internet; ingress is available only from other cluster VMs. Public applications use an outbound tunnel Pod.

Each VM's Zot registry binds only to loopback and is read-only. Containerd can pull and resolve `apps/` and setup-managed `mirror/` images; only the CLI writes image objects.

## Bootstrap

Cloud-init user-data:

- Downloads dependency files from the cluster bucket.
- Verifies checksums.
- Creates system users and groups.
- Extracts and installs files with fixed ownership and permissions.
- Generates runtime configuration and systemd units.
- Enables and starts systemd services.

## Agent

`podmin-agent --provider=aws --bucket=<bucket> --region=<region> --cluster=<cluster-id> --space=<space-id>` runs as a systemd service and:

- Watches the Space revision marker in object storage.
- Syncs Pod specs to kubelet's static-Pod directory.
- Fetches provider secrets and configuration, then mounts them into Pods.

AWS Parameter Store values use `/<cluster-id>/<space-id>/<pod-name>/<key>`. The agent fetches only values declared by Pod annotations and stores them exclusively in tmpfs.

See the [technical specification](./spec.md) for protocols, storage layout, DNS, reconciliation, and failure behavior.
