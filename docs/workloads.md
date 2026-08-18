# Custom Workloads

The built-in manifest is enough for simple single-container workloads. Generate a file when you need to inspect or customize the DaemonSet before deploying it:

```sh
podmin init hello --image hello --nodegroup default --service
podmin validate --file daemonset.yaml --service
podmin deploy hello --nodegroup default --file daemonset.yaml --service
```

`init` refuses to overwrite an existing file. For multiple containers, use named images such as `--image web="$image" --image sidecar="$sidecar_image"`. The generated Service is intentionally available only for a single-container workload; it also sets `TLS_CERT_FILE` and `TLS_KEY_FILE` to the mounted workload certificate paths. Images that support these settings can serve cluster-workload-trusted HTTPS by default. Edit a manifest to define a custom Service, port, or TLS configuration. With `--file`, `--service` asserts that the manifest contains a Service rather than changing it. The same `--image` flags on `validate` or `deploy` set or add image fields by container name.

Every generated and accepted Pod mounts its workload identity read-only at `/var/run/secrets/podmin.dev/tls`. The directory contains `tls.crt`, `tls.key`, and `ca.crt`; the leaf certificate is valid for client authentication and carries a SPIFFE URI for its namespace and Pod name.

`init` defaults to `daemonset.yaml` and the `default` namespace; `--nodegroup` is required. Every VM in the NodeGroup runs the extracted static Pod. A deployment stream is exactly one `apps/v1` DaemonSet plus an optional constrained `v1` Service. To give ready instances a stable name, add a matching label and an inline Service document:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: web
  namespace: product
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      nodeSelector:
        podmin.dev/nodegroup: default
      containers:
        - name: web
          image: <cluster-image>
          readinessProbe:
            tcpSocket:
              port: 80
---
apiVersion: v1
kind: Service
metadata:
  name: frontend
  namespace: product
spec:
  selector:
    app: web
  ports:
    - protocol: TCP
      port: 80
      targetPort: 80
```

Replace `<cluster-image>` with the reference printed by `podmin push`. The Service may have a different name from the DaemonSet, but their namespaces must match; an omitted namespace defaults to `default`. The template's `nodeSelector` must contain only `podmin.dev/nodegroup`; Podmin also rejects `nodeName`, `affinity`, `schedulerName`, and `topologySpreadConstraints`, making the selected NodeGroup the sole scheduling target. Podmin removes the NodeGroup selector while extracting the template, sets the emitted Pod's name and namespace, and adds `podmin.dev/service: frontend` to its annotations. The Service resolves inside the cluster as:

```text
frontend.product.svc.cluster.local
```

Pods search `product.svc.cluster.local`, `svc.cluster.local`, and `cluster.local`, so `frontend`, `frontend.product`, and the full name resolve from this namespace.

Podmin uses kubelet's computed readiness condition, so an unready Pod is removed from new Service flows without duplicating its probe.

To mount existing AWS values, list safe single-component keys in Pod annotations. Values named `/<cluster>/<namespace>/<pod>/<key>` are mounted read-only at `/var/run/podmin/aws-parameter-store/<key>` or `/var/run/podmin/aws-secrets-manager/<key>`; omitted Pod namespaces use `default`:

```yaml
metadata:
  annotations:
    podmin.dev/aws-parameter-store: database-host,log-level
    podmin.dev/aws-secrets-manager: database-password
```

Parameter Store `String`, `StringList`, and decrypted `SecureString` values are supported. Secrets Manager supports both string and binary secret values. `connect --secrets-provider` selects which provider secret commands use by default; `--provider` overrides it for one command.

Podmin opens no ports to the internet. See [Ingress Tunnels](./tunnels.md) to publish an application using managed outbound tunnel Pods.

To deploy your own application, build and push it before updating the manifest:

```sh
podmin build --tag web:v1 \
  --platform linux/amd64 \
  --platform linux/arm64 .
image="$(podmin push web:v1)"
podmin deploy web --nodegroup default --file daemonset.yaml --image "$image"
```

Build every CPU architecture used by the target NodeGroups. By default if no platform is specified, the CLI host machine CPU architecture is used.

Re-running `deploy` publishes the manifest's immutable content revision and reuses unchanged payload objects. The committed global index ETag is used as every static Pod's revision annotation, so a changed deploy or delete currently restarts all synchronized Pods across the cluster, even when their own manifest and image tag are unchanged.

## Next Steps

- [GitHub Actions](./github-actions.md) automates cluster setup and application deployment.
