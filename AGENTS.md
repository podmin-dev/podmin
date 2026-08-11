# Podmin Agent Guide

## Layout

- `cmd/podmin`: CLI entrypoint; command implementation belongs in `internal/cli`.
- `cmd/podmin-agent`: single daemon entrypoint; lifecycle composition belongs in `internal/agent`.
- `internal/agent`: daemon lifecycle and component composition.
- `proto`: protobuf source contracts; generate Go bindings with `make proto`.
- `internal/agent/api`: generated protobuf bindings, gRPC coordination, and loopback HTTP health only.
- `internal/agent/coordinator`: leader election, peer coordination, and service snapshot ownership.
- `internal/agent/identity`: agent CA, node certificates, and mTLS peer identity.
- `internal/agent/workload`: workload CA rotation and Pod certificate issuance.
- `internal/agent/staticpod`: static-Pod reconciliation and Podmin-owned filesystem publication.
- `internal/agent/service`: authoritative DNS and CNI endpoint discovery.
- `internal/cloud`: provider-neutral cloud capabilities used by the CLI.
- `internal/cloud/aws`: shared AWS configuration, clients, and adapters.
- `internal/secrets`: provider-neutral secret naming and management contracts.
- `internal/cli/deploy`: immutable workload publication and desired-state commits.
- `internal/cli/setup`: cluster setup orchestration.
- `internal/cli/infra/<provider>`: CLI-owned embedded OpenTofu/Terraform.
- `internal/cli/userdata`: CLI-owned provider cloud-init scripts and rendering.
- `docs`: user and technical documentation.
- `scripts`: repository automation and Git hooks.

## Commands

- `make setup`: verify tools and install Git hooks.
- `make proto`: generate Go bindings from `proto/*.proto`.
- `make precommit`: fast formatting and shell checks.
- `make lint`: Go, shell, and OpenTofu/Terraform validation.
- `make test`: Go tests with the race detector.
- `make build`: build `bin/podmin` and `bin/podmin-agent`.
- `make website`: render README and `docs/*.md` into `dist/website`.

## Constraints

- Follow `../../podplane/workspace/STANDARDS.md`. Add its yearless header to project-owned source, scripts, workflows, and comment-capable configuration using `Podmin <https://podmin.dev>` and `Copyright The Podmin Authors`; preserve third-party headers.
- Use unmodified upstream kubelet static Pods; do not add a Kubernetes API or control plane.
- Keep the cluster coordination CA separate from the workload CA. Setup create-only stores it at `/<cluster>/_system/cluster-ca`; teardown preserves it and destroy deletes it. Coordination requires TLS 1.3 mTLS.
- Keep `main.go` and `cmd/podmin/main.go` byte-for-byte identical. The former supports `go run github.com/podmin-dev/podmin@latest`; the latter is the canonical, discoverable entrypoint.
- `podmin-agent` has no subcommands. Prefer the standard library, including `log/slog`.
- Every Go function and type requires a doc comment, including unexported and test declarations.
- Prefer SHA-512 when a format or upstream publisher offers it. Preserve required ecosystem digests such as OCI SHA-256.
- Keep provider user-data scripts in `internal/cli/userdata/<provider>.sh`; share rendering and validation where practical.
- OpenTofu is preferred, but embedded infrastructure must validate with both OpenTofu and Terraform.
- GitHub Actions are temporarily stored as `*.yaml.disabled` and must not be enabled without approval.

Generated `dist` output is ignored. Do not commit state, plans, `.terraform`, credentials, or dependency caches.
