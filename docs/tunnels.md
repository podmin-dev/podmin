# Ingress Tunnels

Podmin does not include an ingress controller. You can expose an application by deploying a tunnel client as a Pod. This guide uses a remotely managed [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/setup/).

## Create a Tunnel

Create a remotely managed tunnel in Cloudflare, add a public hostname, and point its service to the application's Podmin DNS name. For example, a Service named `web` in the `default` namespace on port 8080 is:

```text
http://web.default.svc.cluster.local:8080
```

Copy the tunnel token when Cloudflare displays it.

## Copy `cloudflared` image to Podmin cluster bucket

Choose a fixed cloudflared version of 2025.4.0 or later, then copy its multi-platform image into the cluster image store:

```sh
podmin push docker.io/cloudflare/cloudflared:<version>
```

The command prints the image reference to use in the Pod manifest below. Do not use `latest`.

## Store the Tunnel Token

Store the token for a Pod named `cloudflare-tunnel`:

```sh
podmin secret create tunnel-token --for cloudflare-tunnel
```

Enter the token at the hidden prompt. Use `--stdin` to read it from standard input or `--file PATH` to read it from a file. The current context selects Parameter Store or Secrets Manager; `--provider` overrides it. Use the matching Pod annotation. The value is mounted from tmpfs and never written into the Pod manifest.

## Deploy cloudflared

Save the following as `cloudflare-tunnel.yaml`, replacing `<cluster-image>` with the reference printed by `podmin push`:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: cloudflare-tunnel
  namespace: default
spec:
  selector:
    matchLabels:
      app: cloudflare-tunnel
  template:
    metadata:
      labels:
        app: cloudflare-tunnel
      annotations:
        podmin.dev/aws-parameter-store: tunnel-token
    spec:
      nodeSelector:
        podmin.dev/nodegroup: default
      containers:
        - name: cloudflared
          image: <cluster-image>
          imagePullPolicy: Always
          args:
            - tunnel
            - run
            - --token-file
            - /var/run/podmin/aws-parameter-store/tunnel-token
```

Deploy it in the NodeGroup that should run the tunnel connectors:

```sh
podmin deploy cloudflare-tunnel --nodegroup default --file cloudflare-tunnel.yaml
```

Every VM in the NodeGroup runs one connector for the same tunnel. Cloudflare can route through any healthy connector. No inbound firewall rule or public IPv4 address is required.

To reach a Service in another namespace, use its full Podmin DNS name in the Cloudflare service configuration:

```text
http://<service>.<namespace>.svc.cluster.local:<port>
```
