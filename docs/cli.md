# CLI Reference

Podmin supports the following commands:

- `podmin connect <cluster-id> [--provider aws] [--secrets-provider aws-parameter-store|aws-secrets-manager] --region REGION [--profile AWS_PROFILE] --bucket BUCKET`
    - creates the cluster bucket if required and verifies access
    - adds and selects a local context

- `podmin use [<cluster-id>]` selects a previously connected context, or lists all contexts when the cluster ID is omitted

- `podmin disconnect <cluster-id>` removes a context without changing the cluster; clears the current context if selected

- `podmin setup --vpc-cidr CIDR --nodegroup NAME[,size=N][,instance-type=TYPE][,zone=ZONE][,nat64=TYPE] [--nodegroup ...] [--nat64[=instance-type=TYPE]] [--otel-logs endpoint=URL[,grpc=BOOL][,headers-secret=true]] [--workload-ca-publish s3://BUCKET/KEY] [--agent-source PATH] [-y|--auto-approve]` fetches dependencies for every NodeGroup architecture and performs cluster setup and upgrades using OpenTofu/Terraform 1.11 or newer. Setup creates the workload CA key and separate cluster coordination CA directly in Parameter Store when missing. `--vpc-cidr` must be a private IPv4 CIDR. NodeGroup defaults are `size=1`, the first available zone, and, on AWS, `instance-type=t4g.small`; `zone` accepts a full available AWS zone or one suffix character such as `b`. AWS instance types must support IPv6 prefix delegation and at least two network interfaces. One through 256 unique NodeGroups are accepted, less the NAT64 subnets required when NAT64 is enabled. The repeated NodeGroup list is authoritative. Bare `--nat64` uses `t4g.nano` by default; `--nat64=instance-type=TYPE` lets you specify the exact instance type to use. NAT64 enables DNS64 and creates one shared NAT64 instance in every zone used by the NodeGroups. If a `--nodegroup` value includes `nat64=TYPE`, it gives that NodeGroup a dedicated NAT64 instance instead. Every `setup` invocation rotates all NAT64 instances after fully preparing their replacements. The `setup` command downloads the agent matching the CLI version by default; `--agent-source` explicitly builds a development agent from a Podmin checkout.
    - `--otel-logs` installs Fluent Bit as a node service and ships CRI container stdout/stderr. By default (`grpc=false`), `endpoint` is the exact OTLP/HTTP protobuf signal URL and must include the full provider path such as `/v1/logs`. Set `grpc=true` to use OTLP/gRPC; a gRPC endpoint must not include a path. Fluent Bit's OpenTelemetry output does not offer OTLP/HTTP JSON encoding.
    - `headers-secret=true` uses the allowlisted system key `otel-logs-headers`. Create it with `podmin secret create otel-logs-headers --system`; its UTF-8 value must be a JSON object of HTTP header names to string values. Fluent Bit fetches `/<cluster>/_system/otel-logs-headers` from the context's default secrets provider when the service starts, and credentials are not stored in user-data or OpenTofu/Terraform values.
    - `--workload-ca-publish` continuously publishes all retained workload CA certificates as a PEM bundle to the exact existing S3 object. The generated node role receives only `s3:GetObject` and `s3:PutObject` for that object. The bucket owner must separately allow that role and any customer-managed KMS key; Podmin never creates the bucket or deletes the published object.
    - Fluent Bit parses CRI multiline records, includes the source log path, stream, cluster ID, NodeGroup ID, and node hostname as attributes, and uses a bounded 1 GiB filesystem queue during exporter backpressure. Metrics, traces, and host-service logs are not collected.

- `podmin teardown [-y|--auto-approve]` uses OpenTofu/Terraform to remove compute and networking, including resources left by an interrupted setup, while preserving the bucket, workload CA key, cluster CA, and public workload CA state. If an interrupted local operation leaves a state lock, teardown checks for a running local OpenTofu/Terraform process before offering to force-unlock and retry.

- `podmin destroy [-y|--auto-approve]` removes infrastructure, deletes the workload and cluster CAs, empties and removes the cluster bucket, then disconnects its context after confirmation

- `podmin fetch [--agent-source PATH]` resolves the latest host-architecture dependencies and downloads missing or corrupt local cache files. Setup reuses the same resolver and cache validation after comparing its desired set with `dependencies/manifest.json` in cluster object storage. `--agent-source` explicitly builds a development agent from a Podmin checkout.

- `podmin build (-t|--tag) TAG [(-t|--tag) TAG...] [--platform OS/ARCH...] [(-f|--file) FILE] [--pull] [PATH]` builds an OCI image index under `apps/` using [ocimage](https://github.com/podplane/ocimage). `PATH` defaults to `.`, the build-file selection is delegated to ocimage when `--file` is omitted, and the platform defaults to `linux/<CLI host architecture>`.

- `podmin pull SOURCE` downloads a registry image into the local image cache

- `podmin push SOURCE [DESTINATION] [--pull]` uploads a cached or remote OCI image directly to object storage under `apps/`; `mirror/` is reserved for setup-managed images

- `podmin init <name> (--image IMAGE|--image CONTAINER=IMAGE...) (-g|--nodegroup NODEGROUP) [--namespace default] [(-f|--file)=daemonset.yaml] [--service]` creates a minimal `apps/v1` DaemonSet with the standard read-only workload identity mount and refuses to overwrite an existing file. For one image, `--service` also creates a TCP Service on port 443 targeting port 8443, with an HTTPS `/healthz` readiness probe.

- `podmin validate (-f|--file) FILE [--image IMAGE] [--image CONTAINER=IMAGE] [--service]` applies image overrides and validates exactly one `apps/v1` DaemonSet plus an optional constrained `v1` Service without changing the file; `--service` requires that Service to be present.

- `podmin deploy <name> (-g|--nodegroup NODEGROUP) [(-f|--file) FILE] [--image IMAGE] [--image CONTAINER=IMAGE] [(-e|--env) (KEY=VALUE|KEY)]... [--secret KEY]... [--service] [--port SERVICE:TARGET[,SERVICE:TARGET...]]...`
    - deploys the built-in minimal manifest from `--image` unless `--file` is specified
    - built-in manifests accept repeatable environment values (`KEY` inherits the current process) and secret file keys mounted from the context's default provider; duplicate or invalid names fail
    - `--env`, `--secret`, and `--port` are rejected with `--file`, keeping file manifests authoritative
    - with the built-in manifest, `--service` creates a TCP Service on port 443 targeting port 8443 with an HTTPS `/healthz` readiness probe by default; repeat `--port` or use comma-separated mappings to override it, with the first target receiving a TCP readiness probe; with `--file`, `--service` requires a Service already be present and `--port` is rejected
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

Secret operations alternatively accept `--system` instead of `--for`, `--namespace`, and `--provider`. System operations use the context's default secrets provider and permit only explicitly user-manageable keys; currently that allowlist contains `otel-logs-headers`. `secret list --system` hides internal keys such as cluster and workload CAs. Provider-specific deletion and restoration semantics are the same as for workload secrets.

`PROVIDER` is `aws-parameter-store` or `aws-secrets-manager`. `connect` stores the context default, initially `aws-parameter-store`; secret commands use it unless `--provider` overrides it. Parameter Store values must be UTF-8 and at most 4 KiB; deletion is permanent, so use `destroy`. Secrets Manager supports binary values up to 64 KiB, and `delete`/`restore` use its 30-day recovery window.

Commands which access cluster infrastructure or object storage require a current context. Local manifest and image operations do not. Both binaries expose build metadata with `--version` without loading a context.

Cluster and NodeGroup IDs share these rules:

- Lowercase alphanumeric with hyphens
- Must start with a letter
- Must not end with a hyphen
- Maximum 32 characters

The manifest namespace defaults to `default`. `init`, `deploy`, and `delete` use `--nodegroup`/`-g`; secret commands use the Kubernetes-aligned `--namespace`/`-n`, also defaulting to `default`.
