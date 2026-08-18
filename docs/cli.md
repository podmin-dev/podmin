# CLI Reference

Podmin supports the following commands:

- `podmin connect <cluster-id> [--provider aws] [--secrets-provider aws-parameter-store|aws-secrets-manager] --region REGION [--profile AWS_PROFILE] --bucket BUCKET`
    - creates the cluster bucket if required and verifies access
    - adds and selects a local context

- `podmin use <cluster-id>` selects a previously connected context

- `podmin disconnect <cluster-id>` removes a context without changing the cluster; clears the current context if selected

- `podmin setup --vpc-cidr CIDR --nodegroup NAME[,size=N][,instance-type=TYPE] [--nodegroup ...] [--agent-source PATH] [-y|--auto-approve]` fetches dependencies for every NodeGroup architecture and performs idempotent cluster setup and upgrades using OpenTofu/Terraform 1.11 or newer. Setup creates the workload CA key and separate cluster coordination CA directly in Parameter Store when missing. `--vpc-cidr` must be a private IPv4 CIDR. NodeGroup defaults are `size=1` and, on AWS, `instance-type=t4g.small`; AWS instance types must support IPv6 prefix delegation and at least two network interfaces. One through 256 unique NodeGroups are accepted. The repeated NodeGroup list is authoritative. Production setup downloads the agent matching the CLI version; `--agent-source` explicitly builds a development agent from a Podmin checkout.

- `podmin teardown [-y|--auto-approve]` uses OpenTofu/Terraform to remove compute and networking, including resources left by an interrupted setup, while preserving the bucket, workload CA key, cluster CA, and public workload CA state. If an interrupted local operation leaves a state lock, teardown checks for a running local OpenTofu/Terraform process before offering to force-unlock and retry.

- `podmin destroy [-y|--auto-approve]` removes infrastructure, deletes the workload and cluster CAs, empties and removes the cluster bucket, then disconnects its context after confirmation

- `podmin fetch [--agent-source PATH]` resolves the latest host-architecture dependencies and downloads missing or corrupt local cache files. Setup reuses the same resolver and cache validation after comparing its desired set with `dependencies/manifest.json` in cluster object storage. `--agent-source` explicitly builds a development agent from a Podmin checkout.

- `podmin build (-t|--tag) TAG [(-t|--tag) TAG...] [--platform OS/ARCH...] [(-f|--file) FILE] [--pull] [PATH]` builds an OCI image index under `apps/` using [ocimage](https://github.com/podplane/ocimage). `PATH` defaults to `.`, the build-file selection is delegated to ocimage when `--file` is omitted, and the platform defaults to `linux/<CLI host architecture>`.

- `podmin pull SOURCE` downloads a registry image into the local image cache

- `podmin push SOURCE [DESTINATION] [--pull]` uploads a cached or remote OCI image directly to object storage under `apps/`; `mirror/` is reserved for setup-managed images

- `podmin init <name> (--image IMAGE|--image CONTAINER=IMAGE...) (-g|--nodegroup NODEGROUP) [--namespace default] [(-f|--file)=daemonset.yaml] [--service]` creates a minimal `apps/v1` DaemonSet with the standard read-only workload identity mount and refuses to overwrite an existing file. For one image, `--service` also creates a TCP Service and readiness probe on port 8080.

- `podmin validate (-f|--file) FILE [--image IMAGE] [--image CONTAINER=IMAGE] [--service]` applies image overrides and validates exactly one `apps/v1` DaemonSet plus an optional constrained `v1` Service without changing the file; `--service` requires that Service to be present.

- `podmin deploy <name> (-g|--nodegroup NODEGROUP) [(-f|--file) FILE] [--image IMAGE] [--image CONTAINER=IMAGE] [--service]`
    - deploys the built-in minimal manifest from `--image` unless `--file` is specified
    - with the built-in manifest, `--service` creates a TCP Service and readiness probe on port 8080; with `--file`, it requires a Service already be present
    - applies image overrides and the same validation as `validate`
    - uploads immutable Pod and optional Service payloads to content-addressed SHA-512 paths
    - atomically commits the deployment by conditionally updating the cluster-wide deployment index
    - reports a successful desired-state commit; nodes reconcile that state asynchronously

- `podmin delete <name> (-g|--nodegroup NODEGROUP)` atomically removes the deployment from the cluster-wide deployment index

- `podmin list` lists every deployment in committed cluster desired state, including its namespace, NodeGroup, optional Service, and whether it originated from a built-in install command. It does not report runtime Pod health.

- `podmin install cloudflared (-g|--nodegroup) NODEGROUP [--provider PROVIDER]` verifies the predefined `platform-cloudflared/cloudflared/tunnel-token` secret, mirrors Podmin's pinned multi-platform image when it is not already present, and commits one Cloudflare Tunnel connector per NodeGroup VM

- `podmin secret create <key> --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER] [--stdin|--file PATH]` creates a provider secret, prompting securely by default; `--stdin` reads standard input and `--file` reads a file

- `podmin secret update <key> --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER] [--stdin|--file PATH]` updates a provider secret with the same input options

- `podmin secret list --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER]` lists provider secret keys without values

- `podmin secret delete <key> --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER]` archives a provider secret where supported

- `podmin secret restore <key> --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER]` restores an archived provider secret where supported

- `podmin secret destroy <key> --for <pod> [(-n|--namespace) NAMESPACE] [--provider PROVIDER] [-y|--auto-approve]` permanently destroys a provider secret

`PROVIDER` is `aws-parameter-store` or `aws-secrets-manager`. `connect` stores the context default, initially `aws-parameter-store`; secret commands use it unless `--provider` overrides it. Parameter Store values must be UTF-8 and at most 4 KiB; deletion is permanent, so use `destroy`. Secrets Manager supports binary values up to 64 KiB, and `delete`/`restore` use its 30-day recovery window.

Commands which access cluster infrastructure or object storage require a current context. Local manifest and image operations do not. Both binaries expose build metadata with `--version` without loading a context.

Cluster and NodeGroup IDs share these rules:

- Lowercase alphanumeric with hyphens
- Must start with a letter
- Must not end with a hyphen
- Maximum 32 characters

The manifest namespace defaults to `default`. `init`, `deploy`, and `delete` use `--nodegroup`/`-g`; secret commands use the Kubernetes-aligned `--namespace`/`-n`, also defaulting to `default`.
