# Getting Started

This guide creates an AWS cluster and deploys a Pod from your local machine. GitHub Actions automation is covered at the end.

## Prerequisites

Install:

- A pinned [Podmin release](https://github.com/podmin-dev/podmin/releases).
- [OpenTofu](https://opentofu.org/docs/intro/install/) or Terraform. Podmin prefers OpenTofu when both are installed.
- AWS CLI credentials allowed to create the required infrastructure.

Confirm your AWS identity:

```sh
aws sts get-caller-identity
```

Choose a cluster ID, AWS region, and globally unique S3 bucket name.
- The examples use `example`, `us-west-2`, and `example-podmin`.

## Connect to AWS

Create a local context which will create the cluster bucket if it does not already exists:

```sh
podmin connect example \
  --provider aws \
  --region us-west-2 \
  --bucket example-podmin
```

Podmin stores contexts under the applicable XDG data directory, falling back to `~/.podmin`.

`connect` selects the new context automatically, you can later select contexts with the `use` command.

## Create the Cluster

Create one `default` Space containing one ARM64 VM:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --space default
```

Review the OpenTofu/Terraform plan before approving it. Setup creates or reuses a compatible VPC, uploads runtime dependencies, and starts the Space. It is safe to run again for upgrades or configuration changes.

Add Spaces by repeating `--space`:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --space default \
  --space workers,size=3,instance-type=c8g.large
```

The complete Space list is authoritative. Removing a Space from the command removes it after plan approval.

## Deploy an Application

First copy a pinned, multi-platform image into the cluster image store:

```sh
image="$(podmin push docker.io/library/nginx:<pinned-version>)"
```

Generate, validate, and deploy a Pod manifest:

```sh
podmin init web --image "$image" --file pod.yaml
podmin validate --file pod.yaml
podmin deploy web --space default --file pod.yaml
```

`init` refuses to overwrite an existing file. For multiple containers, use named images such as `--image web="$image" --image sidecar="$sidecar_image"`. The same flags on `validate` or `deploy` set or add image fields by container name.

Every VM in the Space runs the Pod. Inside the cluster it resolves as:

```text
web.default.space.cluster.local
```

Podmin opens no ports to the internet. See [Ingress Tunnels](./tunnels.md) to publish the application using an outbound tunnel Pod.

To deploy your own application, build and push it before updating the manifest:

```sh
podmin build --tag web:v1 \
  --platform linux/amd64 \
  --platform linux/arm64 .
image="$(podmin push web:v1)"
podmin deploy web --space default --file pod.yaml --image "$image"
```

Build every CPU architecture used by the target Spaces. By default if no platform is specified, the CLI host machine CPU architecture is used.

Re-running `deploy` advances the Space revision and restarts every instance of the Pod, even when its manifest and image tag are unchanged.

Remove a Pod with:

```sh
podmin delete web --space default
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
            --space default \
            --space workers,size=3,instance-type=c8g.large
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
          podmin deploy web --space default --file pod.yaml --image "$image"
```

The application role needs access only to the cluster image and Space prefixes, plus any secret operations used by that repository.
