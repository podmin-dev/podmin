# Podmin Technical Specification

## Principles

Podmin runs static Pods using an unmodified upstream kubelet, without a Kubernetes API or control plane.

It uses VMs, object storage, and the provider secret store.

AWS is the first provider; provider-specific code stays behind small interfaces.

The agent favours bounded polling, immutable inputs, and the standard library.

## Components

`podmin` is a Cobra CLI. Every command except `connect` and `use` requires a current context.

`podmin-agent` is one daemon with no subcommands and only `--provider`, `--bucket`, `--region`, `--cluster`, `--nodegroup`, and `--ipv6-prefix` configuration flags. The last value is the canonical global-unicast `/80` IPv6 Pod prefix assigned to the VM's primary ENI. It uses `log/slog`, serves health endpoints on loopback, and exits when required configuration is invalid.

Each NodeGroup maps to one Auto Scaling Group. Every VM runs containerd, the gVisor runtime and shim, CNI plugins, upstream kubelet, CoreDNS, read-only Zot, and podmin-agent. runc, a Kubernetes API, CSI components, and inbound management services are absent.

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

## Deployment Manifests

`init <name> --image IMAGE` creates a minimal valid `apps/v1` DaemonSet in `daemonset.yaml` and refuses to overwrite an existing file. `--nodegroup`/`-g` is required, `--namespace` defaults to `default`, and `--file`/`-f` selects another output. One bare image creates a container named after the DaemonSet. Repeated named values create multiple containers:

```sh
podmin init app \
  --nodegroup workers \
  --namespace product \
  --image web=registry.podmin.internal/apps/example/web:v1 \
  --image sidecar=registry.podmin.internal/apps/example/sidecar:v1 \
  --file daemonset.yaml
```

`validate --file daemonset.yaml` performs Podmin's structural validation without writing the manifest. `validate`, `deploy`, and `init` all default to `daemonset.yaml`; `deploy` performs exactly the same extraction, transformation, and validation before upload. A deployment stream contains exactly one `apps/v1` DaemonSet and optionally one inline `v1` Service, in either order. Both commands accept repeated `--image` values. `--image <container>=<reference>` sets or adds that container's image; named values may update a subset. Unknown and duplicate names fail. One bare `--image <reference>` is valid only when the template has one container, and bare and named forms cannot be combined. Every regular and init container must have an image after overrides. Image fields need not exist in the input manifest. Podmin adds the standard workload identity volume to every regular and init container unless the exact reserved read-only mount already exists; conflicting uses of its volume name or path fail validation.

The DaemonSet template must select `podmin.dev/nodegroup: <nodegroup>` in `spec.template.spec.nodeSelector`; deploy requires that value to match `--nodegroup` and `metadata.name` to match `<name>`. No additional node selector is accepted, and `nodeName`, `affinity`, `schedulerName`, and `topologySpreadConstraints` are rejected, making the NodeGroup selector the sole scheduling target. Its namespace is arbitrary and defaults to `default`. Podmin extracts `spec.template` as a `v1` Pod, removes the Podmin NodeGroup selector, sets the Pod name and namespace, applies image overrides, and defaults omitted `imagePullPolicy` to `Always`. The agent later injects the committed global index ETag as `podmin.dev/revision`. Because that ETag belongs to the one cluster-wide index, any successful deploy or delete changes the revision annotation on every synchronized Pod in every NodeGroup, causing kubelet to recreate them even when their own payload is unchanged. Podmin preserves other supported template content and rejects invalid cluster image references with guidance to run `podmin push`.

The optional Service may have a different name from its DaemonSet but must have the same namespace; omitted namespaces default to `default`. Podmin adds `podmin.dev/service: <service>` to the emitted Pod; the NodeGroup selector is not emitted. It deliberately supports only Service `metadata.name`, `metadata.namespace`, a non-empty equality-based `spec.selector` map, and integer TCP or UDP `spec.ports` with optional integer `targetPort`. Protocol defaults to TCP and `targetPort` defaults to `port`; ports range from 1 through 65535 and each protocol/port pair must be unique. Unsupported Kubernetes Service fields, named target ports, and non-TCP/UDP protocols are rejected rather than implying control-plane semantics Podmin does not implement.

Namespaces are honored for Pod metadata, Service matching, endpoint selection, DNS, Service identity, and secret paths. Deployment identity is currently `<nodegroup>/<DaemonSet-name>`, however, and static-Pod filenames use only the Pod name. Two same-named DaemonSets in different namespaces therefore cannot coexist in one NodeGroup; deploying the second replaces the first index entry.

## Infrastructure

Provider source lives in `internal/cli/infra/aws/*.tf` and is embedded with `//go:embed`. It requires OpenTofu or Terraform 1.11 or newer. `setup` replaces the generated module and an `*.auto.tfvars.json` file under `<Podmin cache>/infrastructure/<cluster-id>`, initializes the S3 backend at `tfstate/podmin.tfstate`, plans, and applies after confirmation. The files remain inspectable until the next run or `disconnect`; no generated `.tf` files enter the user's repository. Before applying infrastructure, setup create-only stores a random 32-byte Ed25519 workload CA key at `/<cluster>/_system/workload-ca-key` and the separate cluster coordination CA at `/<cluster>/_system/cluster-ca`. `_system` cannot be a Kubernetes namespace. Neither secret enters OpenTofu/Terraform configuration, plans, outputs, or state. Teardown preserves both CAs and public workload CA state so setup can restore the cluster without changing trust. Destroy deletes both CAs.

`PODMIN_TF_CMD` overrides the executable. Otherwise Podmin searches `PATH` for `tofu`, then `terraform`. Errors and help always call the pair OpenTofu/Terraform.

`setup` accepts a required private IPv4 `--vpc-cidr` and repeated authoritative `--nodegroup` values:

```sh
podmin setup \
  --vpc-cidr 10.0.0.0/16 \
  --nodegroup default \
  --nodegroup workers,size=3,instance-type=c8g.large
```

NodeGroup defaults are `size=1` and `instance-type=t4g.small` (for AWS); the provider API determines architecture. Podmin looks for VPCs whose primary IPv4 CIDR exactly equals the requested CIDR. Zero matches creates one; one compatible match is reused; multiple matches fail. Reuse requires DNS support and hostnames, an attached Amazon-provided IPv6 `/56`, and sufficient free IPv6 `/64` ranges. Incompatibility reports each required change before OpenTofu/Terraform runs.

Workload subnets are IPv6-native `/64`; VMs have no IPv4 address. An IPv6-capable S3 gateway endpoint is restricted to the cluster bucket. AWS uses the official Debian 13 EC2 image. Security groups permit only NodeGroup workload traffic and required agent leader traffic; administration uses provider mechanisms, not SSH exposed to the internet. Instances are tagged `podmin:cluster=<cluster>` and `podmin:nodegroup=<nodegroup>`.

## Object Storage

The cluster bucket is private and object versioning is disabled by default to bound cost. It is laid out as:

```text
tfstate/podmin.tfstate
tfstate/podmin.tfstate.tflock
tfstate/podmin.auto.tfvars.json
dependencies/manifest.json
dependencies/<name>/<versioned-file>
apps/<repository>/oci-layout
apps/<repository>/index.json
apps/<repository>/blobs/sha256/<digest>
mirror/<upstream-registry>/<repository>/oci-layout
mirror/<upstream-registry>/<repository>/index.json
mirror/<upstream-registry>/<repository>/blobs/sha256/<digest>
deployments/index.json
nodegroups/<nodegroup>/pods/sha512/<digest>.yaml
services/<namespace>/<service>/sha512/<digest>.yaml
dns/leader.json
dns/services.pb
identity/ca.json
```

`deployments/index.json` is the single cluster-wide authoritative commit. It maps each `<nodegroup>/<deployment>` directly to a content-addressed Pod path and an optional content-addressed Service path. The SHA-512 digest appears once, in the path, and agents verify fetched bytes against it. Exactly one index entry may own a namespaced `<namespace>/<service>` identity, including across NodeGroups. Deploy creates missing payload objects and then updates the index using ETag compare-and-swap; identical payloads reuse the same objects. Delete only removes its index entry. A competing writer causes a reread and reapplication of the mutation. Failed and orphan payload writes are therefore invisible. The index update atomically publishes the desired set in S3, but node filesystem publication uses sequential per-file atomic renames rather than a cross-file transaction. Agents derive NodeGroup and deployment name from each map key and filter it by NodeGroup.

```json
{
  "workers/web": {
    "pod": "nodegroups/workers/pods/sha512/00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000.yaml",
    "service": "services/product/frontend/sha512/11111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111.yaml"
  }
}
```

`service` is omitted when the deployment has no Service.

## Images

`build --tag TAG` writes an ocimage-compatible local OCI store under `apps/`. It accepts repeated `--platform`; the host platform is the default. Even one platform is represented by an OCI image index, so another architecture can be added without changing the reference. `pull SOURCE` imports a complete remote index. `push SOURCE [DESTINATION]` uses the cache or pulls when absent; `--pull` refreshes it and stdout contains only the resulting cluster image reference. App repositories are normalized beneath `apps/`, matching Podplane. Without a destination, the source registry hostname is replaced by the cluster registry hostname while repository and tag remain beneath that namespace. `mirror/` is reserved for setup-managed dependency images such as the pause image.

Push preserves multi-platform indexes and uploads blobs first, `oci-layout` second, then conditionally merges and writes `index.json` last. A mixed-architecture cluster resolves one shared image reference to the matching manifest. Zot uses no S3 root directory, so repository names map once to object keys. It binds only to `127.0.0.1:5000`; anonymous policy and containerd capabilities permit only pull and resolve, while VM IAM permits read-only access to `apps/` and `mirror/`. All writes go directly from the CLI to object storage. Deduplication, garbage collection, search, and metadata features are disabled. Setup derives the unique architecture set from all NodeGroup instance types and publishes each matching pause image before creating VMs.

These namespaces and OCI layouts match Podplane. A future Podplane existing-registry-bucket option can reuse the bucket without copying images; ownership handoff uses `teardown` and `disconnect`, never `destroy`.

## Dependencies and Bootstrap

One Go file defines dependencies as values with key, pinned semver major, release source, architecture mapping, object name, and digest source. A resolver queries upstream release APIs, rejects other majors and prereleases, and chooses the greatest semantic version. Production setup fetches the published agent matching the CLI version. The explicit `--agent-source` development option instead cross-compiles an agent from the selected Podmin checkout. SHA-512 is required when the publisher provides it; the publisher's strongest available digest is otherwise retained. Files, including Zot's published executable, are uploaded unchanged. OCI content keeps its specified digest, normally SHA-256.

`dependencies/manifest.json` records the complete dependency set published for the cluster. Dependency names and architectures are map keys, supporting mixed-architecture clusters without duplicate entries. `path` is the cluster object-storage path. File sizes are exact bytes; image sizes are unique compressed OCI content bytes reachable from the recorded digest. The generic image map contains today's pause image and can accommodate future image dependencies. For example:

```json
{
  "version": 1,
  "dependencies": {
    "containerd": {
      "amd64": {
        "version": "2.1.4",
        "url": "https://github.com/containerd/containerd/releases/download/v2.1.4/containerd-2.1.4-linux-amd64.tar.gz",
        "path": "dependencies/containerd/containerd-v2.1.4-linux-amd64.tar.gz",
        "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
        "size": 39000000
      },
      "arm64": {
        "version": "2.1.4",
        "url": "https://github.com/containerd/containerd/releases/download/v2.1.4/containerd-2.1.4-linux-arm64.tar.gz",
        "path": "dependencies/containerd/containerd-v2.1.4-linux-arm64.tar.gz",
        "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
        "size": 38000000
      }
    },
    "zot": {
      "arm64": {
        "version": "2.1.8",
        "url": "https://github.com/project-zot/zot/releases/download/v2.1.8/zot-linux-arm64-minimal",
        "path": "dependencies/zot/zot-v2.1.8-linux-arm64",
        "digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
        "size": 29765432
      }
    }
  },
  "images": {
    "pause": {
      "version": "3.10.2",
      "source": "registry.k8s.io/pause:3.10.2",
      "path": "mirror/registry.k8s.io/pause",
      "digest": "sha256:3333333333333333333333333333333333333333333333333333333333333333",
      "size": 1090874
    }
  }
}
```

Generated user-data contains compact rows:

```bash
dependencies=(
  'containerd.tar.gz|dependencies/containerd/containerd-v2.1.4-linux-arm64.tar.gz|sha256:abc...'
  'gvisor.tar.bz2|dependencies/gvisor/gvisor-v20260803-linux-arm64.tar.bz2|sha512:def...'
  'kubelet|dependencies/kubelet/kubelet-v1.36.2-linux-arm64|sha512:def...'
)
```

The template lives at `internal/cli/userdata/aws.sh`; future providers get sibling scripts while sharing Go rendering and validation. It runs one architecture-specific, filtered `aws s3 sync` into `/opt/podmin/downloads`, verifies files, and moves them to canonical paths under `/opt/podmin/dependencies`. The renderer requires the complete canonical dependency set. Fixed idempotent shell extracts containerd without runc, installs the complete gVisor payload, CNI plugins, kubelet, CoreDNS, the unchanged Zot executable, and podmin-agent, then creates users, directories, runtime configuration, and systemd units. One shell function renders the common unit shape with service-specific directives. After `daemon-reload`, enabling the five units and starting kubelet brings up its declared containerd, Zot, agent, and CoreDNS dependencies. Bootstrap needs IMDS, S3, and the regional EC2 API (used to disable source/destination checks); without suitable private endpoints, that means AWS public IPv6 endpoint reachability. AWS CLI performs transfers concurrently.

Launch templates receive deterministic gzip bytes after shell comments and redundant blank lines are removed, then encode those bytes once as required by the provider API. The script records paths and checksums. After successful setup, one paginated list of `dependencies/` is grouped locally by dependency and architecture: retain the newest two versions of every active or previously uploaded architecture; delete third-oldest and older only if every object for that version is at least 28 days old. Cleanup failure warns but does not fail setup.

Setup reads the published manifest and its ETag, resolves the desired files and images for every NodeGroup architecture, and computes the pending set. A manifest match needs no local cache and performs no artifact download or upload. Pending files are checked individually against the local cache; valid files are reused and missing or corrupt files are downloaded with bounded concurrency. Setup uploads only the pending files and images, then conditionally writes the complete manifest last. Uploads interrupted before that commit remain invisible and are safe to repeat. A competing manifest writer fails the ETag comparison. Each NodeGroup's user-data syncs only its own architecture. Removing the last NodeGroup of an architecture does not bypass retention, so rollback remains possible. `fetch` uses the same resolution, cache validation, and download code without reading cluster object storage.

Runtime dependencies are containerd, the complete gVisor archive containing runsc, its shim and sidecar payload, CNI plugins, kubelet, CoreDNS, Zot, podmin-agent, and the pause image. Supported architectures are amd64 and arm64 and one cluster may use both. `crictl` is optional diagnostics, not bootstrap state.

## Testing

User-data unit tests require the canonical dependency set, render each architecture, assert critical runtime configuration, and run `bash -n`; lint additionally runs ShellCheck. Docker bootstrap and full-VM tests are deliberately out of scope for now.

The root agent package owns daemon composition, lifecycle, and host IPv6 discovery. Child packages separate protobuf gRPC and health transport (`internal/agent/api`), leader election and service snapshot coordination (`internal/agent/coordinator`), agent mTLS identity (`internal/agent/identity`), workload PKI (`internal/agent/workload`), kubelet readiness watching (`internal/agent/pods`), static-Pod reconciliation (`internal/agent/staticpod`), authoritative DNS and Service selection (`internal/agent/service`), and eBPF VIP forwarding (`internal/agent/dataplane`). Shared provider-neutral primitives live in `internal/cloud`, with AWS configuration, clients, and adapters in `internal/cloud/aws`; consumers define their own narrow interfaces. In-process tests exercise reconciliation, identity rotation, DNS/coordination, and daemon lifecycle. The committed eBPF objects are verifier-tested on a privileged Linux kernel; full cloud packet-path behavior remains the responsibility of ephemeral-cloud tests.

## Secret and Configuration Mounts

The agent scans annotations on first load after boot, then only after a Pod revision changes:

```yaml
metadata:
  annotations:
    podmin.dev/aws-parameter-store: database-url,log-level
    podmin.dev/aws-secrets-manager: oauth-token
```

Keys must be safe single path components. Both providers resolve values named `/<cluster>/<namespace>/<pod>/<key>`, using the Pod manifest namespace or `default` when it is omitted. Parameter Store accepts `String`, `StringList`, and decrypted `SecureString`, so the mechanism also mounts non-secret configuration. Secrets Manager accepts both `SecretString` and binary-safe `SecretBinary` values. Secrets encrypted with a customer-managed KMS key require an additional `kms:Decrypt` grant on that key for the instance role.

Each provider gets its own read-only volume and path, so equal filenames are valid across providers:

```text
/var/run/podmin/aws-parameter-store/<key>
/var/run/podmin/aws-secrets-manager/<key>
```

Values are atomically written with mode `0400` beneath a host tmpfs and never persisted. The agent copies the submitted manifest, injects hostPath volumes and mounts pointing into tmpfs, then atomically writes each transformed static Pod under the exclusively Podmin-owned `/etc/podmin/manifests` directory. Podmin may therefore remove every stale `*.yaml` file in that directory. A fetch failure retains the last valid Pod and reports unhealthy; undeclared values are never fetched.

## Workload Identity

Every Pod receives `tls.crt`, `tls.key`, and `ca.crt` at the read-only `/var/run/secrets/podmin.dev/tls` mount. Leaf keys and signatures are Ed25519, private keys are unencrypted PKCS#8 PEM for compatibility with current Go/Traefik and OpenSSL/PostgreSQL, and files are written mode `0400` into an immutable generation beneath `/run/podmin/<pod>/identity-generations`. A relative `identity` symlink atomically selects the complete generation. Its certificate fingerprint is added as `podmin.dev/identity-revision`, so kubelet restarts the static Pod onto coherent new files instead of requiring application-specific TLS reload behavior; one previous generation is retained during the switch. Leaves last 24 hours, renew within six hours of expiry, always permit TLS client authentication, and use the Pod name as the subject Common Name. Their URI SAN is `spiffe://<cluster>.podmin.internal/ns/<namespace>/pod/<pod>`. A Pod carrying `podmin.dev/service: <service>` also permits server authentication and receives `<service>.<namespace>.svc.cluster.local`, `<service>.<namespace>.svc`, and `<service>.<namespace>` as DNS SANs. Bare `<service>` is deliberately excluded because it is relative to the client's namespace and could identify different Services.

All agents read the stable 32-byte Ed25519 workload CA key from SSM and retain it only in process memory. Only the elected leader creates or rotates the self-signed public workload CA certificate in `identity/ca.json`; writes use ETag compare-and-swap. Workload CA certificates last one year. Thirty days before expiry, rotation first publishes the next certificate as pending while the old issuer remains active; after a ten-minute propagation interval the leader promotes it and retains the retired certificate for 30 days. The public bundle contains at most two certificates. This ensures workloads trust a new certificate before any leaf uses it and still permits automatic recovery if the normal window was missed. Every agent checks that downloaded workload CA certificates use the stored key before issuing leaves locally, so no workload private key crosses the network or enters object storage. A changed workload CA bundle or approaching leaf expiry triggers index-fenced reconciliation even when the deployment index is unchanged.

`connect --secrets-provider` stores the context's default secrets provider and defaults to `aws-parameter-store`. Secret commands use it unless `--provider` overrides it. They use `--namespace`/`-n`, defaulting to `default`, to address the same namespaced path as the Pod manifest. Parameter Store commands create `SecureString` values, require UTF-8 values no larger than 4 KiB, and reject `delete` and `restore`; `destroy` permanently deletes. Secrets Manager accepts string or binary values up to 64 KiB; `delete` schedules deletion with a 30-day recovery window and `restore` cancels it. `destroy` permanently deletes after confirmation.

## Services and DNS

Defined Services resolve as `<service>.<namespace>.svc.cluster.local`. Deployments without Services do not receive DNS records. Pods search:

```text
<namespace>.svc.cluster.local
svc.cluster.local
cluster.local
```

Thus `database`, `database.product`, and `database.product.svc.cluster.local` work from a Pod in the `product` namespace. CoreDNS binds port 53 on the node's single global IPv6 address, which kubelet supplies as each Pod's DNS server. It forwards `svc.cluster.local` to the agent's authoritative UDP and TCP listeners at `127.0.0.1:1053`; all other names use Debian's provider resolver. The agent serves AAAA (and AAAA content for ANY), returns NXDOMAIN for an unknown Service, and returns one deterministic ULA IPv6 VIP for each namespaced Service with a five-second TTL. The VIP is `fd50:6f64:6d69::/64` plus the first 64 bits of SHA-512 over the NUL-separated cluster, namespace, and Service name. Backend readiness changes do not change DNS.

Kubelet 1.36 runs unmodified with its alpha local Pods API enabled at `/var/lib/kubelet/pods-api/pods-api.sock`. Podmin-agent watches its event stream, selects only namespace- and selector-matching Pods whose kubelet-computed `PodReady` condition is true, and uses each complete update as a coalesced dataplane interface-refresh hint. Liveness still controls kubelet restarts; readiness alone controls new Service flows. The event hint is an optimization rather than a correctness dependency; periodic interface reconciliation covers CNI/status races, reconnects, and missed events.

One agent is elected cluster-wide with `github.com/podplane/s3lect`. Coordination gRPC on each node's IPv6 port 8081 requires TLS 1.3 mutual authentication under a cluster-specific CA. Nodes generate short-lived Ed25519 client/server leaves in memory, renew them before expiry, and use the node global IPv6 as an IP SAN plus one SPIFFE URI SAN binding cluster, node ID, and NodeGroup; Common Name is not authorization. Each Hello reports a canonical global-unicast IPv6 /80 and the leader restricts every endpoint to it. The leader persists deterministic snapshots to `dns/services.pb`; conditional generation and revision writes fence superseded leaders.

Setup create-only stores the separate, long-lived cluster CA secret at `/<cluster>/_system/cluster-ca` outside Terraform state. Teardown preserves it and setup/destroy is the explicit rotation lifecycle; destroy deletes it. Because every node can read the shared cluster CA private key to self-issue, mTLS proves cluster-node membership.

Each VM receives an AWS-delegated IPv6 prefix. Upstream `ptp` plus `host-local` CNI allocates Pod addresses from it without a bridge, overlay, or IP masquerade; Linux retains one `<pod-address>/128` route through each host-side veth and forwards cross-node traffic through the ENI. Every node programs the same unpinned eBPF TCX dataplane on those Pod veths and the lowest-metric IPv6 default-route interface. IPv6 TCP (IP protocol 6) and UDP (17) flows are deterministically assigned to a ready backend, source-NATed to the forwarding node, and reverse-NATed to preserve the VIP tuple. Bidirectional LRU flow maps preserve a selected backend. Maps are bounded to 65,536 service keys, backend entries, and VIPs, and 262,144 forward plus 262,144 reverse flows. The initial parser supports an immediate TCP/UDP header and rejects fragmented Service traffic; extension-header chains are not supported. No-ready-backend traffic fails closed. AWS source/destination checks are disabled and the host reserves ports `30000-32767` for dataplane source NAT; allocation makes at most 32 collision probes. With no Services anywhere, no node IPv6 is required to compile empty state and the dataplane is detached.

## Agent Reconciliation

Startup is: mount tmpfs, start kubelet, start podmin-agent and CoreDNS, restore durable Service DNS without stale backends, discover the leader, then reconcile the NodeGroup. The agent reads the complete `deployments/index.json` and its ETag, filters entries for its NodeGroup, fetches only referenced immutable payloads, verifies every SHA-512 digest, stages the complete Pod and Service candidate set, and confirms the index ETag is unchanged before publication. A missing index means empty desired state; there is no fallback. Missing or corrupt payloads retain previously published state. Pod files are written and synced to same-directory temporary files before changing live paths. It publishes Service desired state only after Pod commits and cleanup succeed. Commits remain sequential atomic renames rather than a cross-file transaction; failures report unhealthy and converge on retry. Work is serialized per NodeGroup and bounded by context deadlines with jittered backoff.

SIGTERM stops new reconciliation, sends drain when possible, waits the configured DNS TTL margin, detaches the dataplane, and exits. During a coordination partition, followers retain their last snapshot for the 35-second lease window and then clear leased backends while retaining Service DNS identities.

## Go Dependencies

Direct third-party packages are deliberately limited:

- `github.com/spf13/cobra`: CLI parsing only.
- `github.com/aws/aws-sdk-go-v2`, its `config`, `feature/ec2/imds`, S3, SSM, and EC2 modules, plus `github.com/aws/smithy-go` for typed AWS errors.
- `github.com/podplane/ocimage`: local builds.
- `github.com/google/go-containerregistry` and `github.com/opencontainers/image-spec`: remote and OCI image transfer.
- `golang.org/x/mod/semver`: pinned-major dependency selection.
- `go.yaml.in/yaml/v3`: strict YAML syntax checks and agent-side static-Pod annotation updates.
- `github.com/miekg/dns`: agent DNS server and wire protocol; CoreDNS uses the same library.
- `github.com/podplane/s3lect`: object-storage leader election.
- `google.golang.org/grpc` and `google.golang.org/protobuf`: typed agent coordination and kubelet's local stream transport.
- `k8s.io/api`, `k8s.io/apimachinery`, and `k8s.io/kubelet`: typed manifest construction and strict decoding plus the version-matched kubelet 1.36 Pods API contract and Pod status types.
- `github.com/cilium/ebpf`: eBPF object loading, maps, and TCX links.
- `github.com/yuin/goldmark`: documentation site generation only (done using `go run` on `./scripts/sitegen`), not included in built binaries.

The standard library provides logging, flags for the agent, loopback HTTP health, embedding, hashing, filesystem operations, process execution, and concurrency. No Kubernetes API server or general Kubernetes client, CSI, WebSocket, Viper, or logging framework is used.

## Roadmap Boundaries

Google Cloud object storage and secrets, metric-based NodeGroup scaling, cloud NLB ingress, IPv4 dual-stack/NAT, and Fluent Bit shipping systemd and Pod logs to S3 or an OpenTelemetry-compatible provider, local VM mode are all out-of-scope/future work.

Podmin's validation boundary is intentionally narrower than Kubernetes: it structurally validates the supported DaemonSet extraction and Service subset, identifiers, image-store references, immutable object paths/digests, and selected cloud topology. It does not provide Kubernetes admission, authorization, NetworkPolicy, general schema/defaulting, or runtime policy enforcement. Workload authors can still request powerful Pod fields preserved in the template, including host-facing mounts or privileges supported by kubelet/gVisor; access to deploy manifests, the cluster bucket, provider secret paths, infrastructure credentials, and the plaintext cluster network must therefore be treated as privileged. Security groups block internet ingress, but the nodes use globally routable IPv6 addresses and permit egress.
