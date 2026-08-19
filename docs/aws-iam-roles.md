# Podmin AWS IAM Roles

This guide covers the recommended permissions and roles to assume when a user or CI build invokes the Podmin CLI.

Users should assume a role through an AWS profile.

CI systems should use short-lived federated credentials instead of stored access keys.

## Recommended Role Boundaries

Use these roles for a production cluster:

| Role | Podmin commands | Recommendation |
| --- | --- | --- |
| Cluster Operator | `setup`, `teardown` | Setup is authoritative and can replace or remove NodeGroups; teardown removes cluster compute and Podmin-managed networking. |
| Super Admin | `destroy` | Recommended to be seperate from the Cluster operation and outside routine automation e.g. only for use by an administrator or break-glass role with an explicit approval step. |
| Deployer | `push`, `deploy`, `delete`, `list` | Preferrably one role per application or deployment pipeline - scope image and immutable workload writes where practical. |

Most teams should start with these three roles. A single role gives every user and CI build infrastructure and permanent-destruction privileges. Separate `push` and `deploy` only when builds and deployments have different owners.

## Configure IAM OIDC Trust Policy to Assume a Role

The following example uses GitHub Actions.

Add GitHub's OIDC provider to AWS with audience `sts.amazonaws.com`, then add this trust policy to the role. Replace the angle-bracketed values:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<account-id>:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": "repo:<owner>/<repository>:environment:production"
        }
      }
    }
  ]
}
```

This example requires `environment: production`. For a branch-bound role, use `repo:<owner>/<repository>:ref:refs/heads/main` instead. Add each allowed subject explicitly; do not use an organization-wide wildcard.

This trust policy allows only the selected GitHub workflows to exchange their OIDC tokens for temporary credentials for the role.

## Create a Deployer Role & Policy

A deployer needs access only to an existing cluster bucket. This example allows it to:

- connect to `<bucket>`;
- push to `apps/<image-repository>/`;
- deploy to NodeGroup `<nodegroup>`; and
- publish Service `<namespace>/<service>`.

Create a role with the following inline policy, replacing each angle-bracketed value and adding resource entries for any additional image repositories, NodeGroups, or Services:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ConnectToClusterBucket",
      "Effect": "Allow",
      "Action": [
        "s3:GetBucketLocation",
        "s3:ListBucket",
        "s3:PutBucketPublicAccessBlock"
      ],
      "Resource": "arn:aws:s3:::<bucket>"
    },
    {
      "Sid": "VerifyClusterBucketAccess",
      "Effect": "Allow",
      "Action": [
        "s3:DeleteObject",
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": "arn:aws:s3:::<bucket>/.podmin-access-check-*"
    },
    {
      "Sid": "PublishApplicationImages",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": "arn:aws:s3:::<bucket>/apps/<image-repository>/*"
    },
    {
      "Sid": "PublishImmutableWorkloads",
      "Effect": "Allow",
      "Action": "s3:PutObject",
      "Resource": [
        "arn:aws:s3:::<bucket>/nodegroups/<nodegroup>/pods/*",
        "arn:aws:s3:::<bucket>/services/<namespace>/<service>/*"
      ]
    },
    {
      "Sid": "ReadDeploymentInventory",
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::<bucket>/nodegroups/*/pods/*"
    },
    {
      "Sid": "CommitDesiredState",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": "arn:aws:s3:::<bucket>/deployments/index.json"
    }
  ]
}
```

The first two statements support `podmin connect`. Use an explicit push destination so the S3 path is predictable:

```sh
podmin push "web:v1" "web:v1"
```

This writes beneath `apps/web/`. Remove `ReadDeploymentInventory` if the role does not run `podmin list`; listing requires every referenced Pod payload and cannot be read-scoped to one NodeGroup.

### Understand the Deployment Scoping Limit

Image, Pod, and Service writes can be prefix-scoped. Deploy and delete cannot: both replace the cluster-wide `deployments/index.json`, and IAM cannot restrict changes within its JSON body. Separate deployer roles therefore do not provide strict application isolation; use separate clusters when required.

### Add Secret Management Permissions

Add these only if the role runs `podmin secret` commands:

| Provider | Actions | Resource |
| --- | --- | --- |
| Parameter Store | `ssm:PutParameter`, `ssm:DeleteParameter`, and, for list, `ssm:GetParametersByPath` | `arn:aws:ssm:<region>:<account-id>:parameter/<cluster>/<namespace>/<app>/*` |
| Secrets Manager | `secretsmanager:CreateSecret`, `secretsmanager:PutSecretValue`, `secretsmanager:DeleteSecret`, `secretsmanager:RestoreSecret`, and, for list, `secretsmanager:ListSecrets` | `arn:aws:secretsmanager:<region>:<account-id>:secret:/<cluster>/<namespace>/<app>/*` |

`secretsmanager:ListSecrets` requires `Resource: "*"`. Customer-managed KMS keys require separately scoped KMS access. Prefer a separate secret-management role.

## Create a Cluster Operator Role & Policy

The cluster operator runs Podmin's AWS SDK calls and embedded OpenTofu/Terraform. Grant access to:

| Area | Access required |
| --- | --- |
| Cluster bucket | Create the named bucket if absent; get its location; apply its public-access block; list it; and read, write, or delete the temporary access-check object. |
| Setup objects | List, read, write, and delete `dependencies/*`; read and write `mirror/*`; read and write `tfstate/podmin.auto.tfvars.json`. |
| OpenTofu/Terraform state | List the bucket and read, write, and delete `tfstate/podmin.tfstate` and its `.tflock` object. |
| Certificate authorities | `ssm:PutParameter` for `/<cluster>/_system/workload-ca-key` and `/<cluster>/_system/cluster-ca`. Setup is create-only and does not need permission to delete them. |
| EC2 networking | Describe VPCs, VPC attributes, availability zones, internet gateways, subnets, instance types, images, and managed resources; create, tag, update, and delete the Podmin VPC when owned, IPv6 CIDR, subnets, route tables and associations, routes, internet gateway and attachment, S3 gateway endpoint, security group and rules. |
| EC2 compute | Create, inspect, update, tag, and delete cluster launch templates and their versions. |
| Auto Scaling | Create, inspect, update, tag, refresh, and delete the cluster's Auto Scaling Groups. |
| Instance identity | Create, inspect, update, and delete the cluster-prefixed EC2 role, inline policy, and instance profile; add or remove the role from the profile; and pass only that role to EC2. |

The AWS provider requires read and refresh actions as well as writes. Many EC2 `Describe*` actions require `Resource: "*"`; restrict them with `aws:RequestedRegion` where supported. Scope IAM access to cluster-prefixed roles and instance profiles, and restrict `iam:PassRole` with `iam:PassedToService: ec2.amazonaws.com`.

Reused VPCs need describe access but should not be deletable. Test the policy in a non-production account and use CloudTrail or IAM Access Analyzer to tighten it after setup and teardown. Repeat this review after Podmin or AWS provider upgrades.

## Create a Super Admin Policy

`podmin teardown` stays with the cluster operator because it preserves the bucket and certificate authorities. For `podmin destroy`, start with teardown permissions and add:

- `ssm:DeleteParameter` for the exact `/<cluster>/_system/workload-ca-key` and `/<cluster>/_system/cluster-ca` parameter ARNs;
- `s3:ListBucket` and `s3:ListBucketVersions` for the cluster bucket;
- `s3:DeleteObject` and `s3:DeleteObjectVersion` for every object in the cluster bucket; and
- `s3:DeleteBucket` for the cluster bucket.

Destroy must access the entire bucket, including versions and delete markers. Keep these permissions out of cluster operator and deployer roles. If GitHub Actions must run destroy, use a dedicated `workflow_dispatch` workflow and a protected environment with required reviewers.

## Next Steps

- See [GitHub Actions](./github-actions.md) for complete workflow examples.
