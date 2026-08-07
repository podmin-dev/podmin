# Podmin - Minimal, Secure Container Platform

Podmin is a deliberately minimal container platform.

Goals:

- Great developer experience with fast deployments.
- Secure and reliable with zero infrastructure maintenance.
- Low cost and minimal cloud services: VMs, object storage, secrets store.

## Features

- Setup a new cluster in your cloud provider one command.
- Build and push container images to object storage, no registry required.
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
- The features of a Kubernetes Pod (e.g. init containers), without using Kubernetes.
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
- AWS Parameter Store only: Secrets Manager (for AWS & Google) planned.
- Auto-Scaling Group size fixed: automatic scaling based on metrics planned.
- Tunnels for ingress: cloud provider NLB support planned.
- IPv6-only NATless: IPv4 dual-stack & NAT configuration planned.
- Observability planned: Fluent Bit shipping VM and Pod logs to S3 or an OpenTelemetry-compatible provider.

## CLI Reference

Podmin CLI supports the following commands:

- `podmin connect <cluster-id> --provider (aws) --region us-west-2 (--profile <aws-profile>) --bucket <cluster bucket>`
    - creates the cluster bucket if required and verifies access
    - adds a "context" for the CLI to connect to
    - switches to that context automatically

- `podmin use <cluster-id>`
    - sets the "current context" of the CLI to a previously connected context
    - same as step 2 of `connect`

- `podmin disconnect <cluster-id>` removes a context without changing the cluster; clears the current context if selected

- `podmin setup (--spaces=default)` fetches and uploads latest dependencies, and performs idempotent cluster set-up and upgrade using OpenTofu/Terraform.

- `podmin teardown` uses OpenTofu/Terraform destroy to remove all resources except cluster bucket

- `podmin destroy` empties and removes the cluster bucket, then disconnects its context.

- `podmin fetch` fetches latest dependencies to local cache
    - same as step 1 of `setup`

- `podmin build` builds a container image using [ocimage](https://github.com/podplane/ocimage).

- `podmin push` uploads a built OCI image directly to object storage.

- `podmin deploy <name> (-s|--space=default) (-f|--file=pod.yaml)`
    - uploads a Pod spec to object storage e.g. `spaces/my-space/pods/deploy-name.yaml`
    - updates the space revision marker

- `podmin delete <name> (-s|--space=default) (-f|--file=pod.yaml)`
    - deletes a Pod spec from object storage e.g. `spaces/my-space/pods/deploy-name.yaml`
    - updates the space revision marker

- `podmin secret create` create a secret provider key/value

- `podmin secret update` update a secret provider key/value

- `podmin secret list` list secret provider keys without showing their values

- `podmin secret delete` archive or destroy secrets provider secrets

- `podmin secret restore` restore an archived secret provider secret key

- `podmin secret destroy` permanently destroy a secrets provider secret

All commands except `connect` and `use` require a current context. All commands require cloud provider access except `use`, `disconnect`, `fetch`, and `build`.

There are only two types of IDs (cluster ID and space ID) with the same validation rules:

- Lowercase alphanumeric with hypens
- Must start with a letter
- Must not end with a hypen
- Max 32 characters

The default space ID is `default`.

## Infrastructure & Agent Reference

`podmin setup --spaces default,example-space,another-space`

- Fetches latest dependencies to local cache
- Uploads latest dependencies to the cluster bucket
- Ensures the Pod sandbox image is available in the cluster registry
- Applies latest OpenTofu/Terraform, with state stored in cluster bucket. Deploys:
    - a VPC, public IPv6 subnet, route tables, security groups, & IAM roles
    - Runs VMs via one Auto-Scaling Group per Podmin Space
    - Embeds a simple cloud-init user-data with pinned dependency versions
- If apply upgrades dependencies/userdata, cloud ASG will rolling-rotate VMs to upgrade

__cloud-init user-data script__

- Downloads all dependency files from the cluster bucket
- Verifies checksums
- Creates appropriate system users and groups
- Decompresses archives and places all files in correct locations
- Applies appropriate file permissions
- Starts systemd services

`podmin-agent --space=<space-id>` runs as a systemd service and:

- Watches/polls the space revision marker in object storage
- For deployments, syncs the latest Pod specs to the Kubelet static Pods directory
- Fetches secrets from the cloud provider secrets store and mounts them into Pods

Secret mounts:

- Requires all secrets to be stored at a fixed prefix
- e.g. for AWS Parameter Store `/<cluster-id>/<space-id>/<pod-name>/<secret-key>`
- Stores fetched values only in tmpfs

## License

Podmin is licensed under the Apache License, Version 2.0.
Copyright The Podmin Authors.

See the [LICENSE](./LICENSE) file for details.
