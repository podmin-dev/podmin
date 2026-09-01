# Podmin - Minimal, Secure Container Platform

Podmin is a deliberately minimal container platform.

- `setup` creates/updates your cluster and its Node Groups (VMs), including all infrastruucture.
- `push` stores your container images in object storage.
- `deploy` deploys your app as a DaemonSet to a Node Group
- Podmin runs [kubelet static pods](https://kubernetes.io/docs/concepts/workloads/pods/static-pods/) on each VM in that Node Group.
- There is no Kubernetes control plane, only `kubelet`.

The key constraint of Podmin is that you must run the same collection of DaemonSets/Pods on each VM within a Node Group.

- This may be less of a constraint than you may think.
- For example, if you want to run a single Pod, you can run a single-VM Node Group for a few dollars per month.

## Goals

- Great developer experience with fast deployments.
- Secure and reliable with zero infrastructure maintenance.
- Low cost and minimal cloud services: VMs, object storage, secrets store.

## Features

- Set up a new cluster in your cloud provider with one command.
- Divide a cluster into multiple NodeGroups to scale different sets of Pods on multiple VMs.
- Build and push multi-arch container images to object storage, with no managed registry service.
- Deploy pushed images using a Kubernetes-compatible DaemonSet manifest to run as Pods on each NodeGroup node.
- Deploy a Kubernetes-compatible Service for stable DNS and readiness-aware load balancing to Pods.
- Mount encrypted secrets from your cloud provider secrets store using host tmpfs.
- Every Pod gets a short-lived SPIFFE-compatible client certificate
- Pods associated with a Service also receive a certificate valid for their Service DNS name.
- Use public IPv6 subnets without exposing inbound ports to the internet.
- Mount ephemeral volumes from your VM.
- Podmin only supports AWS for now, but has a deliberately cloud agnostic design.

## How It Works

It only has two components, a CLI and a minimal agent process:

`podmin` CLI:
- Deploys infrastructure to your cloud provider using OpenTofu/Terraform.
- Lets you easily build, push, and deploy apps as Pods.

`podmin-agent`:
- Runs on a VM alongside containerd, gVisor, kubelet, and CoreDNS.
- Watches manifests, loads secrets, and updates kubelet static Pods.
- Serves a node-local read-only registry from S3 itself.
- Gives Pods direct-routed IPv6 addresses from each VM's delegated prefix using upstream `ptp` and `host-local` CNI plugins; there is no bridge, overlay, or Pod NAT.
- Reuses kubelet readiness, coordinates ready Service endpoints over gRPC, and programs an eBPF VIP dataplane only when Services exist.
- Keeps Service discovery opt-in: without inline Services, the eBPF dataplane remains inactive and no Service DNS or endpoint state is produced.
- Issues workload certificates locally from a CA key held in the provider secret store and rotates public workload CA certificates through object storage.
- Protects agent coordination with TLS 1.3 mutual authentication and renewable, in-memory node certificates issued under a separate cluster CA.

Podmin has the concept of NodeGroups:
- Each NodeGroup can run multiple DaemonSet deployments.
- One NodeGroup equates to one Auto Scaling Group.
- Each DaemonSet runs one extracted static Pod on every VM in its NodeGroup.
- Optional Services use Kubernetes-style `<service>.<namespace>.svc.cluster.local` names across the cluster.

## When To Use

Use when you want:

- Fast container deployments on a simple, reliable, no-maintenance platform.
- The features of a Kubernetes Pod (e.g. init containers), without a Kubernetes API or control plane.
- Horizontal scaling of Pods across a set of VMs, using cloud provider VM auto-scaling.
- The ability to securely mount secrets from your cloud provider secrets store.
- A low cost solution for running containers that only takes a few minutes to set up.

Do not use when you want:

- Independent Pod scaling within the same NodeGroup
- On-demand Pods
- Persistent volumes
- Kubernetes

When you need more from your container platform, check out [Podplane](https://podplane.dev) Kubernetes PaaS.

Podmin aims to implement a subset of Podplane manifests and CLI commands, to ease migration to Podplane at a later date.

## Scope & Roadmap

- AWS-only: Google Cloud planned.
- AWS Parameter Store and AWS Secrets Manager mounts; Google Secret Manager planned.
- Auto-Scaling Group size fixed: automatic scaling based on metrics planned.
- Ingress via the built-in Cloudflare Tunnel installer or user-deployed tunnel Pods; cloud provider NLB support is planned.
- Observability planned: Fluent Bit shipping VM and Pod logs to S3 or an OpenTelemetry-compatible provider.

## Documentation

- [Getting Started](./docs/getting-started.md): provision a cluster and deploy your first application.
- [Ingress Tunnels](./docs/tunnels.md): expose an application with the built-in cloudflared component.
- [Custom Workloads](./docs/workloads.md): customize manifests, Services, secrets, and images.
- [GitHub Actions](./docs/github-actions.md): automate cluster setup and application deployment.
- [CLI Reference](./docs/cli.md): commands, options, contexts, and identifier rules.
- [Infrastructure and Agent](./docs/infra.md): setup, bootstrap, networking, and agent behavior.
- [Technical Specification](./docs/spec.md): architecture, protocols, storage, and implementation boundaries.

## Development

Run `make setup`, then `make precommit lint test build`. OpenTofu/Terraform validate infrastructure modules; ShellCheck and `bash -n` validate shell and rendered user-data. Releases use immutable semantic-version tags, GoReleaser, SHA-512 checksums, SBOMs, provenance, and keyless signing.

## License

Podmin is licensed under the Apache License, Version 2.0.
Copyright The Podmin Authors.

See the [LICENSE](./LICENSE) file for details.
