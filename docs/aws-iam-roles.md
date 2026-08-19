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
        "s3:GetBucketPublicAccessBlock",
        "s3:GetBucketLocation",
        "s3:ListBucket"
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

The first two statements support `podmin connect`. Connect reads the public-access block and changes it only when protection is missing. Because the deployer cannot make that change, an unsafe bucket causes connect to fail until a Cluster Operator repairs it.

Use an explicit push destination so the S3 path is predictable:

```sh
podmin push "web:v1" "web:v1"
```

This writes beneath `apps/web/`. Remove `ReadDeploymentInventory` if the role does not run `podmin list`; listing requires every referenced Pod payload and cannot be read-scoped to one NodeGroup.

### Understand the Deployment Scoping Limit

Image, Pod, and Service writes can be prefix-scoped. Deploy and delete cannot: both replace the cluster-wide `deployments/index.json`, and IAM cannot restrict changes within its JSON body. Separate deployer roles therefore do not provide strict application isolation; use separate clusters when required.

### Add Secret Management Permissions

Add this policy only if the role runs `podmin secret` commands. Replace the angle-bracketed values, then remove statements for providers or operations it does not use:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ManageParameterStoreSecrets",
      "Effect": "Allow",
      "Action": [
        "ssm:DeleteParameter",
        "ssm:GetParametersByPath",
        "ssm:PutParameter"
      ],
      "Resource": "arn:aws:ssm:<region>:<account-id>:parameter/<cluster>/<namespace>/<app>/*"
    },
    {
      "Sid": "ManageSecretsManagerSecrets",
      "Effect": "Allow",
      "Action": [
        "secretsmanager:CreateSecret",
        "secretsmanager:DeleteSecret",
        "secretsmanager:PutSecretValue",
        "secretsmanager:RestoreSecret"
      ],
      "Resource": "arn:aws:secretsmanager:<region>:<account-id>:secret:/<cluster>/<namespace>/<app>/*"
    },
    {
      "Sid": "ListSecretsManagerSecrets",
      "Effect": "Allow",
      "Action": "secretsmanager:ListSecrets",
      "Resource": "*"
    }
  ]
}
```

Customer-managed KMS keys require separately scoped KMS access. Prefer a separate secret-management role.

## Create a Cluster Operator Role & Policy

The Cluster Operator runs Podmin's AWS SDK calls and embedded OpenTofu/Terraform. Replace the angle-bracketed values in this starting policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ManageClusterBucket",
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:GetBucketLocation",
        "s3:GetBucketPublicAccessBlock",
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
      "Sid": "ManageSetupAndStateObjects",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": [
        "arn:aws:s3:::<bucket>/dependencies/*",
        "arn:aws:s3:::<bucket>/mirror/*",
        "arn:aws:s3:::<bucket>/tfstate/*"
      ]
    },
    {
      "Sid": "DeleteExpiredDependenciesAndStateLocks",
      "Effect": "Allow",
      "Action": "s3:DeleteObject",
      "Resource": [
        "arn:aws:s3:::<bucket>/dependencies/*",
        "arn:aws:s3:::<bucket>/tfstate/*"
      ]
    },
    {
      "Sid": "CreateClusterCertificateAuthorities",
      "Effect": "Allow",
      "Action": "ssm:PutParameter",
      "Resource": [
        "arn:aws:ssm:<region>:<account-id>:parameter/<cluster>/_system/workload-ca-key",
        "arn:aws:ssm:<region>:<account-id>:parameter/<cluster>/_system/cluster-ca"
      ]
    },
    {
      "Sid": "ReadEC2State",
      "Effect": "Allow",
      "Action": [
        "ec2:Describe*",
        "ec2:GetInstanceUefiData"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "<region>"
        }
      }
    },
    {
      "Sid": "ManageEC2Infrastructure",
      "Effect": "Allow",
      "Action": [
        "ec2:AssociateRouteTable",
        "ec2:AssociateSubnetCidrBlock",
        "ec2:AssociateVpcCidrBlock",
        "ec2:AttachInternetGateway",
        "ec2:AuthorizeSecurityGroupEgress",
        "ec2:AuthorizeSecurityGroupIngress",
        "ec2:CreateInternetGateway",
        "ec2:CreateLaunchTemplate",
        "ec2:CreateLaunchTemplateVersion",
        "ec2:CreateRoute",
        "ec2:CreateRouteTable",
        "ec2:CreateSecurityGroup",
        "ec2:CreateSubnet",
        "ec2:CreateTags",
        "ec2:CreateVpc",
        "ec2:CreateVpcEndpoint",
        "ec2:DeleteInternetGateway",
        "ec2:DeleteLaunchTemplate",
        "ec2:DeleteLaunchTemplateVersions",
        "ec2:DeleteRoute",
        "ec2:DeleteRouteTable",
        "ec2:DeleteSecurityGroup",
        "ec2:DeleteSubnet",
        "ec2:DeleteTags",
        "ec2:DeleteVpc",
        "ec2:DeleteVpcEndpoints",
        "ec2:DetachInternetGateway",
        "ec2:DisassociateRouteTable",
        "ec2:DisassociateSubnetCidrBlock",
        "ec2:DisassociateVpcCidrBlock",
        "ec2:ModifyLaunchTemplate",
        "ec2:ModifySecurityGroupRules",
        "ec2:ModifySubnetAttribute",
        "ec2:ModifyVpcAttribute",
        "ec2:ModifyVpcEndpoint",
        "ec2:ReplaceRoute",
        "ec2:ReplaceRouteTableAssociation",
        "ec2:RevokeSecurityGroupEgress",
        "ec2:RevokeSecurityGroupIngress"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "<region>"
        }
      }
    },
    {
      "Sid": "ManageAutoScalingGroups",
      "Effect": "Allow",
      "Action": [
        "autoscaling:CancelInstanceRefresh",
        "autoscaling:CreateAutoScalingGroup",
        "autoscaling:CreateOrUpdateTags",
        "autoscaling:DeleteAutoScalingGroup",
        "autoscaling:DeleteTags",
        "autoscaling:Describe*",
        "autoscaling:StartInstanceRefresh",
        "autoscaling:UpdateAutoScalingGroup"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "<region>"
        }
      }
    },
    {
      "Sid": "ManageClusterInstanceIdentity",
      "Effect": "Allow",
      "Action": [
        "iam:AddRoleToInstanceProfile",
        "iam:CreateInstanceProfile",
        "iam:CreateRole",
        "iam:DeleteInstanceProfile",
        "iam:DeleteRole",
        "iam:DeleteRolePolicy",
        "iam:GetInstanceProfile",
        "iam:GetRole",
        "iam:GetRolePolicy",
        "iam:ListAttachedRolePolicies",
        "iam:ListInstanceProfileTags",
        "iam:ListInstanceProfilesForRole",
        "iam:ListRolePolicies",
        "iam:ListRoleTags",
        "iam:PutRolePolicy",
        "iam:RemoveRoleFromInstanceProfile",
        "iam:TagInstanceProfile",
        "iam:TagRole",
        "iam:UntagInstanceProfile",
        "iam:UntagRole",
        "iam:UpdateAssumeRolePolicy",
        "iam:UpdateRole",
        "iam:UpdateRoleDescription"
      ],
      "Resource": [
        "arn:aws:iam::<account-id>:role/<cluster>-*",
        "arn:aws:iam::<account-id>:instance-profile/<cluster>-*"
      ]
    },
    {
      "Sid": "PassClusterInstanceRole",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": "arn:aws:iam::<account-id>:role/<cluster>-*",
      "Condition": {
        "StringEquals": {
          "iam:PassedToService": "ec2.amazonaws.com"
        }
      }
    },
    {
      "Sid": "CreateAutoScalingServiceLinkedRole",
      "Effect": "Allow",
      "Action": "iam:CreateServiceLinkedRole",
      "Resource": "arn:aws:iam::<account-id>:role/aws-service-role/autoscaling.amazonaws.com/AWSServiceRoleForAutoScaling",
      "Condition": {
        "StringEquals": {
          "iam:AWSServiceName": "autoscaling.amazonaws.com"
        }
      }
    }
  ]
}
```

The regional EC2 and Auto Scaling actions use `Resource: "*"` because several required APIs cannot be resource-scoped. Test this policy in a non-production account, then tighten it with CloudTrail or IAM Access Analyzer. Recheck it after Podmin or AWS provider upgrades.

## Create a Super Admin Policy

`podmin teardown` stays with the cluster operator because it preserves the bucket and certificate authorities. For `podmin destroy`, start with teardown permissions and add:

- `ssm:DeleteParameter` for the exact `/<cluster>/_system/workload-ca-key` and `/<cluster>/_system/cluster-ca` parameter ARNs;
- `s3:ListBucket` and `s3:ListBucketVersions` for the cluster bucket;
- `s3:DeleteObject` and `s3:DeleteObjectVersion` for every object in the cluster bucket; and
- `s3:DeleteBucket` for the cluster bucket.

Destroy must access the entire bucket, including versions and delete markers. Keep these permissions out of cluster operator and deployer roles. If GitHub Actions must run destroy, use a dedicated `workflow_dispatch` workflow and a protected environment with required reviewers.

## Next Steps

- See [GitHub Actions](./github-actions.md) for complete workflow examples.
