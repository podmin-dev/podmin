# Podmin Technical Specification

## Principles

Podmin runs static Pods using an unmodified upstream kubelet, without a Kubernetes API or control plane.

It uses VMs, object storage, and the provider secret store.

AWS is the first provider; provider-specific code stays behind small interfaces.

The agent favours bounded polling, immutable inputs, and the standard library.

## Components

`podmin` is a Cobra CLI. Every command except `connect` and `use` requires a current context.

`podmin-agent` is one daemon with no subcommands and only provider, bucket, region, cluster ID, and Space ID flags. It uses `log/slog`, serves health endpoints on loopback, and exits when required configuration is invalid.

Each Space maps to one Auto Scaling Group. Every VM runs containerd, the gVisor runtime and shim, CNI plugins, upstream kubelet, CoreDNS, read-only Zot, and podmin-agent. runc, a Kubernetes API, CSI components, and inbound management services are absent.

## CLI State and Contexts

Contexts contain cluster ID, provider, region, bucket, and optional AWS profile. Defaults:

| Class | Linux | macOS/Windows |
| --- | --- | --- |
| Config | `${XDG_CONFIG_HOME:-~/.config}/podmin` | `~/.podmin/config` |
| Cache | `${XDG_CACHE_HOME:-~/.cache}/podmin` | `~/.podmin/cache` |
| Data | `${XDG_DATA_HOME:-~/.local/share}/podmin` | `~/.podmin/data` |
| Runtime | `$XDG_RUNTIME_DIR/podmin`, else `~/.podmin/run` | `~/.podmin/run` |

An explicitly set XDG variable wins on every OS. Config writes use a temporary file, `fsync`, rename, and mode `0600`.

`connect` creates the object storage bucket when absent, verifies its region and read/write/delete access, saves the context, and selects it. `use` selects an existing context. `disconnect` removes local state only. `teardown` destroys compute and networking but retains the bucket and context. `destroy` empties and deletes the bucket, then removes its context only after success.

## Pod Manifests

`init <name> --image IMAGE --file pod.yaml` creates a minimal valid Pod and refuses to overwrite an existing file. One bare image creates a container named after the Pod. Repeated named values create multiple containers:

```sh
podmin init app \
  --image web=registry.podmin.internal/apps/example/web:v1 \
  --image sidecar=registry.podmin.internal/apps/example/sidecar:v1 \
  --file pod.yaml
```

`validate --file pod.yaml` performs Podmin's structural validation without writing the manifest. `deploy` performs exactly the same transformation and validation before upload. Both accept repeated `--image` values. `--image <container>=<reference>` sets or adds that container's image; named values may update a subset. Unknown and duplicate names fail. One bare `--image <reference>` is valid only when the Pod has one container, and bare and named forms cannot be combined. Every regular and init container must have an image after overrides. Image fields need not exist in the input manifest.

Validation requires `apiVersion: v1`, `kind: Pod`, a valid metadata name, non-empty uniquely named containers, and the fields Podmin must transform safely. It preserves unknown YAML nodes and ordering. `deploy <name>` additionally requires `metadata.name` to equal `<name>`. Invalid cluster image references direct the user to `podmin push`.

## Infrastructure

Provider source lives in `internal/infra/aws/*.tf` and is embedded with `//go:embed`. `setup` writes it and an `*.auto.tfvars.json` file into a private temporary directory, initializes the S3 backend at `tfstate/podmin.tfstate`, plans, applies after confirmation, then removes the directory. No generated `.tf` files enter the user's repository or Podmin state directories.

`PODMIN_TF_CMD` overrides the executable. Otherwise Podmin searches `PATH` for `tofu`, then `terraform`. Errors and help always call the pair OpenTofu/Terraform.

`setup` accepts a required private IPv4 `--vpc-cidr` and repeated authoritative Space values:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --space default \
  --space workers,size=3,instance-type=c8g.large
```

Space defaults are `size=1` and `instance-type=t4g.small` (for AWS); the provider API determines architecture. Podmin looks for VPCs whose primary IPv4 CIDR exactly equals the requested CIDR. Zero matches creates one; one compatible match is reused; multiple matches fail. Reuse requires DNS support and hostnames, an attached Amazon-provided IPv6 `/56`, and sufficient free IPv6 `/64` ranges. Incompatibility reports each required change before OpenTofu/Terraform runs.

Workload subnets are IPv6-native `/64`; VMs have no IPv4 address. An IPv6-capable S3 gateway endpoint is restricted to the cluster bucket. AWS uses the official Debian 13 EC2 image. Security groups permit only Space workload traffic and required agent leader traffic; administration uses provider mechanisms, not SSH exposed to the internet.

## Object Storage

The cluster bucket is private and object versioning is disabled by default to bound cost. It is laid out as:

```text
tfstate/podmin.tfstate
tfstate/podmin.tfstate.tflock
dependencies/<name>/<versioned-file>
dependencies/user-data/<UTC-timestamp>.sh
apps/<repository>/oci-layout
apps/<repository>/index.json
apps/<repository>/blobs/sha256/<digest>
mirror/<upstream-registry>/<repository>/oci-layout
mirror/<upstream-registry>/<repository>/index.json
mirror/<upstream-registry>/<repository>/blobs/sha256/<digest>
spaces/<space>/revision
spaces/<space>/pods/<deployment>.yaml
dns/leader.json
dns/endpoints.json
```

Pod objects are authoritative. Every deploy or delete replaces `revision` with a random token, even when bytes and image tags are unchanged. Agents poll its metadata/ETag; a change triggers one list and reconciliation. The revision is injected into the rendered static Pod so kubelet recreates it. An omitted `imagePullPolicy` becomes `Always`; an explicit value is preserved.

## Images

`build --tag TAG` writes an ocimage-compatible local OCI store under `apps/`. It accepts repeated `--platform`; the host platform is the default. Even one platform is represented by an OCI image index, so another architecture can be added without changing the reference. `pull SOURCE` imports a complete remote index. `push SOURCE [DESTINATION]` uses the cache or pulls when absent; `--pull` refreshes it and stdout contains only the resulting cluster image reference. App repositories are normalized beneath `apps/`, matching Podplane. Without a destination, the source registry hostname is replaced by the cluster registry hostname while repository and tag remain beneath that namespace. `mirror/` is reserved for setup-managed dependency images such as the pause image.

Push preserves multi-platform indexes and uploads blobs first, `oci-layout` second, then conditionally merges and writes `index.json` last. A mixed-architecture cluster resolves one shared image reference to the matching manifest. Zot uses no S3 root directory, so repository names map once to object keys. It binds only to `127.0.0.1:5000`; anonymous policy and containerd capabilities permit only pull and resolve, while VM IAM permits read-only access to `apps/` and `mirror/`. All writes go directly from the CLI to object storage. Deduplication, garbage collection, search, and metadata features are disabled. Setup derives the unique architecture set from all Space instance types and publishes each matching pause image before creating VMs.

These namespaces and OCI layouts match Podplane. A future Podplane existing-registry-bucket option can reuse the bucket without copying images; ownership handoff uses `teardown` and `disconnect`, never `destroy`.

## Dependencies and Bootstrap

One Go file defines dependencies as values with key, pinned semver major, release source, architecture mapping, object name, and digest source. A resolver queries upstream release APIs, rejects other majors and prereleases, and chooses the greatest semantic version. SHA-512 is required when the publisher provides it; the publisher's strongest available digest is otherwise retained. OCI content keeps its specified digest, normally SHA-256. There is no dependency manifest file.

Generated user-data contains compact rows:

```bash
dependencies=(
  'containerd.tar.gz|dependencies/containerd/containerd-v2.1.4-linux-arm64.tar.gz|sha512:abc...'
  'gvisor.tar.bz2|dependencies/gvisor/gvisor-v20260803-linux-arm64.tar.bz2|sha512:def...'
  'kubelet|dependencies/kubelet/kubelet-v1.36.2-linux-arm64|sha512:def...'
)
```

The template lives at `internal/userdata/aws.sh`; future providers get sibling scripts while sharing Go rendering and validation. It runs one architecture-specific, filtered `aws s3 sync` into `/opt/podmin/downloads`, verifies files, and moves them to canonical paths under `/opt/podmin/dependencies`. The renderer requires the complete canonical dependency set. Fixed idempotent shell extracts containerd without runc, installs the complete gVisor payload, CNI plugins, kubelet, CoreDNS, Zot, and podmin-agent, then creates users, directories, runtime configuration, and systemd units. One shell function renders the common unit shape with service-specific directives. After `daemon-reload`, enabling the five units and starting kubelet brings up its declared containerd, Zot, agent, and CoreDNS dependencies. Boot therefore needs IMDS and S3 but not the public internet. AWS CLI performs transfers concurrently.

Setup archives changed user-data as `dependencies/user-data/<UTC-timestamp>.sh`, with its SHA-512 in object metadata and the Auto Scaling Group launch template. Names use `20060102T150405.000000000Z.sh`; a conditional create retries with a new timestamp on collision. The latest object's SHA-512 prevents archiving unchanged bytes. The archived script retains source comments for auditability; launch templates receive deterministic gzip bytes after shell comments and redundant blank lines are removed, then encode those bytes once as required by the provider API. The script records paths and checksums. After successful setup, one paginated list of `dependencies/` is grouped locally by dependency and architecture: retain the newest two versions of every active or previously uploaded architecture; delete third-oldest and older only if every object for that version is at least 28 days old. Apply the same count-and-age rule to timestamped user-data. Cleanup failure warns but does not fail setup.

Setup resolves every Space instance type before fetching. It downloads and uploads each dependency once per required architecture; each Space's user-data syncs only its own architecture. Removing the last Space of an architecture does not bypass retention, so rollback remains possible.

Runtime dependencies are containerd, the complete gVisor archive containing runsc, its shim and sidecar payload, CNI plugins, kubelet, CoreDNS, Zot, podmin-agent, and the pause image. Supported architectures are amd64 and arm64 and one cluster may use both. `crictl` is optional diagnostics, not bootstrap state.

## Testing

User-data unit tests require the canonical dependency set, render each architecture, assert critical runtime configuration, and run `bash -n`; lint additionally runs ShellCheck. Docker bootstrap and full-VM tests are deliberately out of scope for now.

Agent packages isolate object storage, provider APIs, filesystems, clocks, and leader election behind small interfaces tested with in-process fakes. A process-level integration test starts the real agent with temporary static-Pod and tmpfs directories, exercises reconciliation and DNS/HTTP endpoints, then verifies graceful SIGTERM shutdown. Cloud-specific networking, IMDS, IAM, systemd, kubelet, gVisor, and CNI behavior remain the responsibility of future local-VM and ephemeral-cloud tests.

## Secret and Configuration Mounts

The agent scans annotations on first load after boot, then only after a Pod revision changes:

```yaml
metadata:
  annotations:
    podmin.dev/aws-parameter-store: database-url,log-level
    podmin.dev/aws-secrets-manager: oauth-token # reserved for planned support
```

Keys must be safe single path components. AWS paths are `/<cluster>/<space>/<pod>/<key>`. Parameter Store accepts `String`, `StringList`, and decrypted `SecureString`, so the mechanism also mounts non-secret configuration. Secrets Manager will use the same contract when implemented.

Each provider gets its own read-only volume and path, so equal filenames are valid once both providers are available:

```text
/var/run/podmin/aws-parameter-store/<key>
/var/run/podmin/aws-secrets-manager/<key>
```

Values are atomically written with mode `0400` beneath a host tmpfs and never persisted. The agent copies the submitted manifest, injects hostPath volumes and mounts pointing into tmpfs, then atomically writes the transformed static Pod. A fetch failure retains the last valid Pod and reports unhealthy; undeclared values are never fetched.

`secret create` creates Parameter Store `SecureString` by default. `delete` follows provider archive semantics; Parameter Store rejects it and directs users to `destroy`. `restore` is unsupported there. `destroy` permanently deletes after confirmation.

## DNS

Deployments resolve as `<deployment>.<space>.space.cluster.local`. Pods search:

```text
<current-space>.space.cluster.local
space.cluster.local
cluster.local
```

Thus `database`, `database.workers`, and the full name work. CoreDNS binds port 53 on the node's single private IPv6 address, which kubelet supplies as each Pod's DNS server. It forwards only `space.cluster.local` to the agent at `127.0.0.1:1053`; all other names use Debian's provider resolver. The agent implements authoritative DNS using `miekg/dns` and returns all live IPv6 Pod addresses for a deployment with a five-second TTL.

One agent is elected cluster-wide with `github.com/podplane/s3lect`. Every agent reads the CNI result cache and sends its current deployment-to-Pod-IP map to the leader with `PUT /v1/dns/nodes/<instance-id>` after reconciliation or an address change. The leader validates the source against the provider instance record, keeps a complete endpoint map in memory, and persists it to `dns/endpoints.json`. Followers load that snapshot before starting DNS, then receive complete snapshots over standard-library HTTP SSE at `GET /v1/dns/events`. Graceful shutdown sends `POST /v1/dns/drain`; the VM remains alive for DNS TTL plus margin.

Only the leader polls Auto Scaling and EC2. It removes stopped or missing instances, repairing missed drains and partitions; agents refresh their registration every ten seconds. Provider errors never publish an empty snapshot. Followers retain their last valid snapshot while disconnected. Snapshots carry leadership term and revision; obsolete terms are rejected. Losing the object-storage lease immediately stops publication. Poll and lease intervals must account for s3lect's effective frequent tick interval.

## Agent Reconciliation

Startup order is: mount tmpfs, load last DNS snapshot, start DNS and health endpoints, discover the leader, then reconcile the Space. Reconciliation validates a complete candidate before changing live files. Writes use temporary files, `fsync`, rename, and directory `fsync`; stale static Pods and secret directories are removed only after the new set is durable. Work is serialized per Space and bounded by context deadlines with jittered backoff.

SIGTERM stops new reconciliation, sends drain when possible, removes this node from DNS, waits the configured TTL margin, then exits. A network partition falls back to provider polling and object-storage snapshots.

## Go Dependencies

Direct third-party packages are deliberately limited:

- `github.com/spf13/cobra`: CLI parsing only.
- `github.com/aws/aws-sdk-go-v2/config` and service modules for S3, SSM, EC2, and Auto Scaling.
- `github.com/podplane/ocimage`: local builds.
- `github.com/google/go-containerregistry` and `github.com/opencontainers/image-spec`: remote and OCI image transfer.
- `golang.org/x/mod/semver`: pinned-major dependency selection.
- `go.yaml.in/yaml/v3`: node-preserving Pod transformation.
- `github.com/containernetworking/cni`: decoding cached CNI results for DNS.
- `github.com/miekg/dns`: agent DNS wire protocol.
- `github.com/podplane/s3lect`: object-storage leader election.
- `github.com/yuin/goldmark`: documentation site generation only (done using `go run` on `./scripts/sitegen`), not included in built binaries.

The standard library provides logging, flags for the agent, HTTP/SSE, JSON, embedding, hashing, filesystem operations, process execution, and concurrency. No gRPC, WebSocket, Kubernetes client, CSI, Viper, or logging framework is used.

## Roadmap Boundaries

Google Cloud object storage and secrets, AWS Secrets Manager, metric-based Space scaling, cloud NLB ingress, IPv4 dual-stack/NAT, and Fluent Bit shipping systemd and Pod logs to S3 or an OpenTelemetry-compatible provider, local VM mode are all out-of-scope/future work.
