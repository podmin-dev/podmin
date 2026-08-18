# GitHub Actions

Podmin's local commands work unchanged in CI systems. Authenticate CI builds using OIDC federation rather than long-lived access keys.

GitHub Actions can use [AWS OIDC federation](https://docs.github.com/actions/deployment/security-hardening-your-deployments/configuring-openid-connect-in-amazon-web-services).

For each repository:

1. Add GitHub's OIDC provider to AWS with audience `sts.amazonaws.com`.
2. Create a least-privilege IAM role.
3. Restrict its trust policy to the exact repository and branch using the OIDC `sub` claim.
4. Add `AWS_ROLE_ARN`, `AWS_REGION`, `PODMIN_CLUSTER`, and `PODMIN_BUCKET` as GitHub variables.

## Automate Cluster Setup

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

## Automate Application Deployment

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

## Further Reading

Learn more about how Podmin [Infrastructure and Agent](./infra.md) work under the hood.
