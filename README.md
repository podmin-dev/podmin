# Podmin - Minimal, Secure Container Platform

Podmin is a deliberately minimal container platform.

Goals:

- Great developer experience with fast deployments.
- Secure and reliable with zero infrastructure maintenance.
- Low cost and minimal cloud services: VMs, object storage, secrets store.

## Features

- Setup a new cluster in your cloud provider one command.
- Build and push multi-arch container images to object storage, no registry required.
- Deploy pushed images using a Kubernetes Pod spec.
- Mount encrypted secrets from your cloud provider secrets store.
- Mount ephemeral volumes from your VM.
- Divide a cluster into multiple Spaces to scale different sets of pods on multiple VMs.
- Podmin only supports AWS for now, but has a deliberately cloud agnostic design.

## How It Works

It only has two components, a CLI and a minimal agent process:

`podmin` CLI:
- Deploys infrastructure to your cloud provider using OpenTofu/Terraform.
- Let's you easily build, push, and deploy apps as Pods.

`podmin-agent`:
- Runs on a VM alongside containerd, gVisor, kubelet, CoreDNS, and Zot.
- Watches for changes to manifests, load secrets, updates kubelet static Pods.

Podmin has the concept of "Spaces":
- Each Space can run multiple Pods.
- One Space equates to one Auto-Scaling Group.
- The same set of Pods run on all VMs in each Space.

## When To Use

Use when you want:

- Fast container deployments on a simple, reliable, no-maintenance platform.
- The ability to securely mount secrets from your cloud provider secrets store.
- The features of a Kubernetes Pod (e.g. init containers), without a Kubernetes API or control plane.
- Horizontal scaling of Pods across a set of VMs, using cloud provider VM auto-scaling.
- A low cost solution for running containers that only takes a few minutes to set up.

Do not use when you want:

- Independent Pod scaling within the same space
- On-demand Pods
- Persistent volumes
- Kubernetes

When you need more from your container platform, check out [Podplane](https://podplane.dev) Kubernetes PaaS.

## Scope & Roadmap

- AWS-only: Google Cloud planned.
- AWS Parameter Store only: AWS Secrets Manager and Google Secret Manager planned.
- Auto-Scaling Group size fixed: automatic scaling based on metrics planned.
- Ingress via user-deployed tunnel Pods: cloud provider NLB support planned.
- IPv6-only NATless: IPv4 dual-stack & NAT configuration planned.
- Observability planned: Fluent Bit shipping VM and Pod logs to S3 or an OpenTelemetry-compatible provider.

## Documentation

- [Getting Started](./docs/getting-started.md): provision a cluster and deploy an application locally or from CI.
- [CLI Reference](./docs/cli.md): commands, options, contexts, and identifier rules.
- [Infrastructure and Agent](./docs/infra.md): setup, bootstrap, networking, and agent behavior.
- [Ingress Tunnels](./docs/tunnels.md): expose an application with a tunnel Pod.
- [Technical Specification](./docs/spec.md): architecture, protocols, storage, and implementation boundaries.

## Development

Run `make setup`, then `make precommit lint test build`. OpenTofu and Terraform validate infrastructure modules; ShellCheck and `bash -n` validate shell and rendered user-data. Releases use immutable semantic-version tags, GoReleaser, SHA-512 checksums, SBOMs, provenance, and keyless signing.

## License

Podmin is licensed under the Apache License, Version 2.0.
Copyright The Podmin Authors.

See the [LICENSE](./LICENSE) file for details.
