# Getting Started

This guide creates an AWS cluster and deploys a DaemonSet as static Pods from your local machine. GitHub Actions automation is covered at the end.

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
aws sts get-caller-identity
```

Choose a cluster ID, AWS region, and globally unique S3 bucket name.
- The examples use `example`, `us-west-2`, and `example-podmin`.

## Connect to AWS

Create a local context which will create the cluster bucket if it does not already exist:

```sh
podmin connect example \
  --provider aws \
  --region us-west-2 \
  --bucket example-podmin
```

Podmin stores contexts under the applicable XDG config directory, falling back to `~/.podmin`.

`connect` selects the new context automatically, you can later select contexts with the `use` command.

## Create the Cluster

Create one `default` NodeGroup containing one ARM64 VM:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --nodegroup default
```

Review the OpenTofu/Terraform plan before approving it. Setup creates or reuses a compatible VPC, compares runtime dependencies with the cluster manifest, transfers only the pending files and images, and starts the NodeGroup. It is safe to run again for upgrades or configuration changes, including from CI without a persistent local cache.

Add NodeGroups by repeating `--nodegroup`:

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

Deploy it to the `default` NodeGroup with Podmin's built-in Service:

```sh
podmin deploy hello --image hello --nodegroup default --service
```

`--service` adds an opinionated TCP Service and readiness probe on port 8080. The application is now available inside the cluster at:

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

Note: This inventory does not claim that the asynchronously reconciled Pod is running or healthy.

See [Ingress Tunnels](./tunnels.md) to make this Hello service available through a Cloudflare Tunnel.

To inspect or customize the manifest before deploying it, generate a file instead:

```sh
podmin init hello --image hello --nodegroup default --service
podmin validate --file daemonset.yaml --service
podmin deploy hello --nodegroup default --file daemonset.yaml --service
```

`init` refuses to overwrite an existing file. For multiple containers, use named images such as `--image web="$image" --image sidecar="$sidecar_image"`. The generated Service is intentionally available only for a single-container workload; edit a manifest to define a custom Service or port. With `--file`, `--service` asserts that the manifest contains a Service rather than changing it. The same `--image` flags on `validate` or `deploy` set or add image fields by container name.

Every generated and accepted Pod mounts its workload identity read-only at `/var/run/secrets/podmin.dev/tls`. The directory contains `tls.crt`, `tls.key`, and `ca.crt`; the leaf certificate is valid for client authentication and carries a SPIFFE URI for its namespace and Pod name.

`init` defaults to `daemonset.yaml` and the `default` namespace; `--nodegroup` is required. Every VM in the NodeGroup runs the extracted static Pod. A deployment stream is exactly one `apps/v1` DaemonSet plus an optional constrained `v1` Service. To give ready instances a stable name, add a matching label and an inline Service document:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: web
  namespace: product
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      nodeSelector:
        podmin.dev/nodegroup: default
      containers:
        - name: web
          image: <cluster-image>
          readinessProbe:
            tcpSocket:
              port: 80
---
apiVersion: v1
kind: Service
metadata:
  name: frontend
  namespace: product
spec:
  selector:
    app: web
  ports:
    - protocol: TCP
      port: 80
      targetPort: 80
```

Replace `<cluster-image>` with the reference printed by `podmin push`. The Service may have a different name from the DaemonSet, but their namespaces must match; an omitted namespace defaults to `default`. The template's `nodeSelector` must contain only `podmin.dev/nodegroup`; Podmin also rejects `nodeName`, `affinity`, `schedulerName`, and `topologySpreadConstraints`, making the selected NodeGroup the sole scheduling target. Podmin removes the NodeGroup selector while extracting the template, sets the emitted Pod's name and namespace, and adds `podmin.dev/service: frontend` to its annotations. The Service resolves inside the cluster as:

```text
frontend.product.svc.cluster.local
```

Pods search `product.svc.cluster.local`, `svc.cluster.local`, and `cluster.local`, so `frontend`, `frontend.product`, and the full name resolve from this namespace.

Podmin uses kubelet's computed readiness condition, so an unready Pod is removed from new Service flows without duplicating its probe.

To mount existing AWS values, list safe single-component keys in Pod annotations. Values named `/<cluster>/<namespace>/<pod>/<key>` are mounted read-only at `/var/run/podmin/aws-parameter-store/<key>` or `/var/run/podmin/aws-secrets-manager/<key>`; omitted Pod namespaces use `default`:

```yaml
metadata:
  annotations:
    podmin.dev/aws-parameter-store: database-host,log-level
    podmin.dev/aws-secrets-manager: database-password
```

Parameter Store `String`, `StringList`, and decrypted `SecureString` values are supported. Secrets Manager supports both string and binary secret values. `connect --secrets-provider` selects which provider secret commands use by default; `--provider` overrides it for one command.

Podmin opens no ports to the internet. See [Ingress Tunnels](./tunnels.md) to publish an application using managed outbound tunnel Pods.

To deploy your own application, build and push it before updating the manifest:

```sh
podmin build --tag web:v1 \
  --platform linux/amd64 \
  --platform linux/arm64 .
image="$(podmin push web:v1)"
podmin deploy web --nodegroup default --file daemonset.yaml --image "$image"
```

Build every CPU architecture used by the target NodeGroups. By default if no platform is specified, the CLI host machine CPU architecture is used.

Re-running `deploy` publishes the manifest's immutable content revision and reuses unchanged payload objects. The committed global index ETag is used as every static Pod's revision annotation, so a changed deploy or delete currently restarts all synchronized Pods across the cluster, even when their own manifest and image tag are unchanged.

Remove a Pod with:

```sh
podmin delete web --nodegroup default
```

## Automate with GitHub Actions

The local commands above work unchanged in CI systems.

You should aim to authenticate CI builds using OIDC federation rather than long-lived access keys.

- GitHub Actions can use [AWS OIDC federation](https://docs.github.com/actions/deployment/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services).

For each repository:

1. Add GitHub's OIDC provider to AWS with audience `sts.amazonaws.com`.
2. Create a least-privilege IAM role.
3. Restrict its trust policy to the exact repository and branch using the OIDC `sub` claim.
4. Add `AWS_ROLE_ARN`, `AWS_REGION`, `PODMIN_CLUSTER`, and `PODMIN_BUCKET` as GitHub variables.

### Automate Cluster Setup

An infrastructure repository can run setup daily and on-demand with:

```yaml
name: Podmin setup
on:
  workflow_dispatch:
  schedule:
    - cron: "17 3 * * *"
permissions:
  contents: read
  id-token: write
jobs:
  setup:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version: 1.26.2
          cache: false
      - uses: opentofu/setup-opentofu@a1320f892987e89d278cc92dc5adc984fb93aca4 # v2.0.2
        with:
          tofu_version: 1.12.0
      - uses: aws-actions/configure-aws-credentials@e3dd6a429d7300a6a4c196c26e071d42e0343502 # v6
        with:
          role-to-assume: ${{ vars.AWS_ROLE_ARN }}
          aws-region: ${{ vars.AWS_REGION }}
      - run: go install github.com/podmin-dev/podmin@v0.1.0
      - run: |
          podmin connect "${{ vars.PODMIN_CLUSTER }}" \
            --provider aws \
            --region "${{ vars.AWS_REGION }}" \
            --bucket "${{ vars.PODMIN_BUCKET }}"
          podmin setup \
            --vpc-cidr 10.0.0.0/16 \
            --nodegroup default \
            --nodegroup workers,size=3,instance-type=c8g.large
```

Update the pinned Podmin and OpenTofu versions deliberately. Review the workflow, IAM permissions, and initial OpenTofu/Terraform plan before enabling the schedule. `PODMIN_TF_CMD` can select a specific executable; otherwise Podmin searches for `tofu`, then `terraform`.

### Automate Application Deployment

An application repository can build, push, and deploy on each change:

```yaml
name: Deploy
on:
  workflow_dispatch:
  push:
    branches: [main]
permissions:
  contents: read
  id-token: write
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version: 1.26.2
          cache: false
      - uses: aws-actions/configure-aws-credentials@e3dd6a429d7300a6a4c196c26e071d42e0343502 # v6
        with:
          role-to-assume: ${{ vars.AWS_ROLE_ARN }}
          aws-region: ${{ vars.AWS_REGION }}
      - run: go install github.com/podmin-dev/podmin@v0.1.0
      - run: |
          podmin connect "${{ vars.PODMIN_CLUSTER }}" \
            --provider aws \
            --region "${{ vars.AWS_REGION }}" \
            --bucket "${{ vars.PODMIN_BUCKET }}"
          podmin build --tag "web:${GITHUB_SHA}" \
            --platform linux/amd64 \
            --platform linux/arm64 .
          image="$(podmin push "web:${GITHUB_SHA}")"
          podmin deploy web --nodegroup default --file daemonset.yaml --image "$image"
```

The application role needs access only to the cluster image, `deployments/`, `nodegroups/`, and `services/` prefixes, plus any secret operations used by that repository.
