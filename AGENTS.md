# Podmin Agent Guide

## Layout

- `cmd/podmin`: CLI entrypoint; command implementation belongs in `internal/cli`.
- `cmd/podmin-agent`: single daemon entrypoint; implementation belongs in `internal/agent`.
- `internal/infra/<provider>`: embedded OpenTofu/Terraform.
- `internal/userdata`: provider cloud-init scripts and rendering.
- `docs`: user and technical documentation.
- `scripts`: repository automation and Git hooks.

## Commands

- `make setup`: verify tools and install Git hooks.
- `make precommit`: fast formatting and shell checks.
- `make lint`: Go, shell, OpenTofu, and Terraform validation.
- `make test`: Go tests with the race detector.
- `make build`: build `bin/podmin`.
- `make website`: render README and `docs/*.md` into `dist/website`.

## Constraints

- Follow `../../podplane/workspace/STANDARDS.md`. Add its yearless header to project-owned source, scripts, workflows, and comment-capable configuration using `Podmin <https://podmin.dev>` and `Copyright The Podmin Authors`; preserve third-party headers.
- Use unmodified upstream kubelet static Pods; do not add a Kubernetes API or control plane.
- Keep `main.go` and `cmd/podmin/main.go` byte-for-byte identical. The former supports `go run github.com/podmin-dev/podmin@latest`; the latter is the canonical, discoverable entrypoint.
- `podmin-agent` has no subcommands. Prefer the standard library, including `log/slog`.
- Every Go function and type requires a doc comment, including unexported and test declarations.
- Prefer SHA-512 when a format or upstream publisher offers it. Preserve required ecosystem digests such as OCI SHA-256.
- Keep provider user-data scripts in `internal/userdata/<provider>.sh`; share rendering and validation where practical.
- OpenTofu is preferred, but embedded infrastructure must validate with both OpenTofu and Terraform.
- GitHub Actions are temporarily stored as `*.yaml.disabled` and must not be enabled without approval.

Generated `dist` output is ignored. Do not commit state, plans, `.terraform`, credentials, or dependency caches.
