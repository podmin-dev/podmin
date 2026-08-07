# CLI Reference

Podmin supports the following commands:

- `podmin connect <cluster-id> --provider (aws) --region us-west-2 (--profile <aws-profile>) --bucket <cluster-bucket>`
    - creates the cluster bucket if required and verifies access
    - adds and selects a local context

- `podmin use <cluster-id>` selects a previously connected context

- `podmin disconnect <cluster-id>` removes a context without changing the cluster; clears the current context if selected

- `podmin setup --vpc-cidr 10.0.0.0/16 [--space default]` fetches dependencies for every Space architecture and performs idempotent cluster setup and upgrades using OpenTofu/Terraform

- `podmin teardown` uses OpenTofu/Terraform to remove all resources except the cluster bucket

- `podmin destroy` empties and removes the cluster bucket, then disconnects its context

- `podmin fetch` fetches the latest dependencies to the local cache; this is also the first step of `setup`

- `podmin build --tag TAG [--platform linux/amd64] [--platform linux/arm64]` builds an OCI image index under `apps/` using [ocimage](https://github.com/podplane/ocimage); the host platform is the default

- `podmin pull SOURCE` downloads a registry image into the local image cache

- `podmin push SOURCE [DESTINATION]` uploads a cached or remote OCI image directly to object storage under `apps/`; `mirror/` is reserved for setup-managed images

- `podmin init <name> (--image IMAGE|--image CONTAINER=IMAGE...) (-f|--file=pod.yaml)` creates a minimal Pod manifest and refuses to overwrite an existing file

- `podmin validate (-f|--file=pod.yaml) [--image IMAGE] [--image CONTAINER=IMAGE]` applies image overrides and validates a Pod manifest without changing it

- `podmin deploy <name> (-s|--space=default) (-f|--file=pod.yaml) [--image IMAGE] [--image CONTAINER=IMAGE]`
    - applies image overrides and the same validation as `validate`
    - uploads the Pod spec to `spaces/<space>/pods/<name>.yaml`
    - updates the Space revision marker

- `podmin delete <name> (-s|--space=default)` deletes the Pod spec and updates the Space revision marker

- `podmin secret create <key> --for <pod> (-s|--space=default)` creates a provider secret

- `podmin secret update <key> --for <pod> (-s|--space=default)` updates a provider secret

- `podmin secret list --for <pod> (-s|--space=default)` lists provider secret keys without values

- `podmin secret delete <key> --for <pod> (-s|--space=default)` archives a provider secret where supported

- `podmin secret restore <key> --for <pod> (-s|--space=default)` restores an archived provider secret where supported

- `podmin secret destroy <key> --for <pod> (-s|--space=default)` permanently destroys a provider secret

All commands except `connect` and `use` require a current context. All commands require cloud provider access except `use`, `disconnect`, `fetch`, `build`, `pull`, `init`, and `validate`. Both binaries expose build metadata with `--version` without loading a context.

Cluster and Space IDs share these rules:

- Lowercase alphanumeric with hyphens
- Must start with a letter
- Must not end with a hyphen
- Maximum 32 characters

The default Space ID is `default`.
