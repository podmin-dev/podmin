# Infrastructure and Agent

## Setup

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --nodegroup default \
  --nodegroup workers,size=3,instance-type=c8g.large
```

Setup:

- Reads `dependencies/manifest.json` and resolves the desired dependency set for every NodeGroup architecture.
- Downloads or uploads only files and images absent from the published manifest, reusing each digest-validated local cache entry independently.
- Publishes the complete dependency manifest last using an ETag conditional write.
- Ensures required Pod sandbox images are available in the cluster image store.
- Reuses the VPC whose primary IPv4 CIDR exactly matches `--vpc-cidr`, or creates one that `destroy` later deletes; incompatible or ambiguous matches fail. Reused VPCs are never deleted by Podmin.
- Saves the generated infrastructure configuration before applying OpenTofu/Terraform, whose state is stored in the cluster bucket. An interrupted setup can therefore be removed with `podmin teardown`.
- Creates the workload CA key and cluster CA key directly in SSM SecureStrings when missing; neither enters OpenTofu/Terraform state. Teardown preserves both and destroy deletes both.
- Creates or reuses a VPC, then creates public IPv6 subnets, route tables, security groups, IAM roles, and one Auto Scaling Group per NodeGroup. Each VM receives a node-address ENI and a Pod-prefix ENI declared by its launch template.
- Waits up to three minutes for each Auto Scaling Group to reach its desired healthy capacity.
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
- Routes delegated-prefix traffic through the Pod ENI.
- Enables and starts systemd services.

## Agent

`podmin-agent --provider=aws --bucket=<bucket> --region=<region> --cluster=<cluster-id> --nodegroup=<nodegroup-id> --ipv6-prefix=<delegated-prefix>` runs as a systemd service and:

- **Deployment reconciliation**
  - Watches the cluster-wide `deployments/index.json` and filters deployments for its NodeGroup.
  - Syncs Pod specs to the Podmin-owned kubelet static-Pod directory, `/etc/podmin/manifests`.
- **Provider secrets and configuration (optional)**
  - Fetches declared AWS Parameter Store and Secrets Manager values and mounts them into Pods.
  - Uses `/<cluster-id>/<namespace>/<pod-name>/<key>`, with omitted Pod namespaces defaulting to `default`.
- **Workload identity**
  - Reads the fixed workload CA key from the reserved `/<cluster-id>/_system/workload-ca-key` Parameter Store SecureString and keeps private keys only in memory or tmpfs. `_system` cannot be a Kubernetes namespace. Teardown preserves the key; destroy deletes it.
  - Issues short-lived Pod certificates into immutable generations and mounts the selected generation read-only at `/var/run/secrets/podmin.dev/tls`.
- **Service discovery (optional)**
  - Watches kubelet's event-driven local Pods API and selects ready matching Pod IPv6 addresses.
  - Coordinates complete endpoint snapshots over TLS 1.3 mutual-authenticated gRPC and publishes stable Service VIPs through CoreDNS.
  - Reads the separate cluster CA from `/<cluster-id>/_system/cluster-ca` and keeps renewable node certificates and private keys only in memory.
- **Direct IPv6 Pod networking**
  - Uses separate launch-template ENIs for the node IPv6 address and delegated Pod prefix.
  - Allocates addresses from the delegated prefix with upstream `ptp` and `host-local` CNI plugins.
  - Gives each Pod a host-side `/128` route without a bridge, overlay, or Pod NAT.
- **Service dataplane (optional)**
  - Loads an unpinned eBPF IPv6 TCP/UDP dataplane only while the cluster snapshot contains Services.
  - Discovers Pod veths and the Pod ENI from delegated-prefix routes and attaches to them and the node's lowest-metric IPv6 default-route interface.
  - Uses kubelet events as coalesced refresh hints and retains a periodic convergence scan.
  - Supports Services from other NodeGroups; an empty cluster snapshot detaches the dataplane.

AWS instances and ENIs carry `podmin:cluster` and `podmin:nodegroup` tags. Their IAM role reads cluster-scoped Parameter Store and Secrets Manager values plus `dependencies/`, `apps/`, `mirror/`, `deployments/`, `nodegroups/`, `services/`, `dns/`, and `identity/` in S3. S3 writes are limited to `dns/` and public workload CA state under `identity/`.

Secrets Manager values encrypted with a customer-managed KMS key additionally require the instance role to receive `kms:Decrypt` for that key; Podmin does not grant access to arbitrary customer keys.

See the [technical specification](./spec.md) for protocols, storage layout, DNS, reconciliation, and failure behavior.
