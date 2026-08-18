# Getting Started

This guide creates an AWS cluster and deploys an example "hello" app on your cluster via the `podmin` CLI.

## Prerequisites

Install Podmin with Homebrew:

```sh
brew install podmin-dev/tap/podmin
```

Or install the latest release with Go:

```sh
go install github.com/podmin-dev/podmin@latest
```

Prebuilt binaries are available from [Podmin releases](https://github.com/podmin-dev/podmin/releases). You also need:

- OpenTofu 1.11 or Terraform 1.11 or newer. Podmin prefers OpenTofu when both are installed. The workload and cluster coordination CAs are created directly in Parameter Store and never enter OpenTofu/Terraform state.
- AWS CLI credentials allowed to create the required infrastructure.

Confirm your AWS identity:

```sh
aws sts get-caller-identity --profile=demo
```

## Connect to AWS

You will need to decide on a cluster ID, AWS region, and globally unique S3 bucket name.

For this guide, the examples use:
- Cluster ID: `example`
- Region: `us-west-2`
- Bucket Name: `example-podmin`.

First, you need to create a local "context" - this will create the cluster bucket if it does not already exist:

```sh
podmin connect example \
  --provider aws \
  --region us-west-2 \
  --bucket example-podmin
```

Podmin stores contexts under the applicable XDG config directory, falling back to `~/.podmin`.

`podmin connect` selects the new context automatically, but you can later switch between connected contexts with the `podmin use` command.

## Create the Cluster

Create one `default` NodeGroup containing one ARM64 VM:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --nodegroup default
```

Review the OpenTofu/Terraform plan before approving it. Setup creates or reuses a compatible VPC, compares runtime dependencies with the cluster manifest, transfers only the pending files and images, and starts the NodeGroup. It is safe to run again for upgrades or configuration changes, including from CI without a persistent local cache.

You can add/remove NodeGroups at any time. Add multiple by repeating `--nodegroup`:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --nodegroup default \
  --nodegroup workers,size=3,instance-type=c8g.large
```

The complete NodeGroup list is authoritative. Removing a NodeGroup from the command removes it after plan approval.

## Deploy an Application

Copy Podplane's multi-platform Hello image into the cluster image store under the short name `hello`:

```sh
podmin push ghcr.io/podplane/hello:latest hello
```

Deploy it to the `default` NodeGroup with Podmin's default/built-in manifest:

```sh
podmin deploy hello --image hello --nodegroup default --service
```

The default/built-in manifest includes a DaemonSet, and `--service` includes an opinionated TCP Service with a readiness probe on port 8080.

- You can customise the manifest (and Service ports) by specifying a manifest file using `-f` (we'll cover this later in [Custom Workloads](./workloads.md)).

The application is now available inside the cluster (from other Pods) at:

```text
http://hello.default.svc.cluster.local:8080
```

List the cluster's committed desired state at any time:

```sh
podmin list
```

```text
NAME   NAMESPACE  NODEGROUP  SERVICE  ORIGIN
hello  default    default    hello    workload
```

Note: Podmin does not have a control plane, so the `podmin list` command only tells you the desired state configured in your object storage bucket - this inventory does not claim that the asynchronously reconciled Pod is running or healthy.

## Next Steps

- [Ingress Tunnels](./tunnels.md) makes the Hello service available through a Cloudflare Tunnel.
- [CLI Reference](./cli.md) details every command and argument for the Podmin CLI.
- [Custom Workloads](./workloads.md) covers custom DaemonSet manifests, Services, secrets, and multi-platform images.
- [GitHub Actions](./github-actions.md) automates cluster setup and application deployment.

## Clean Up

When finished, you can remove the app you deployed from the cluster's desired state:

```sh
podmin delete hello --nodegroup default
```

Run `podmin list` again to confirm that the deployment is no longer committed. Nodes remove the static Pod asynchronously.

Remove compute and networking while retaining the cluster bucket and certificate authorities so the cluster can be recreated later:

```sh
podmin teardown
```

Permanently remove all cluster infrastructure, stored workloads, images, and certificate authorities, then disconnect the local context:

```sh
podmin destroy
```

Both commands show what they will remove and require confirmation before proceeding.
