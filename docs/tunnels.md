# Ingress Tunnels

Podmin does not include an ingress controller. You can expose an application by deploying a tunnel client as a Pod. This guide uses a remotely managed [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/setup/).

## Create a Tunnel

Create a remotely managed tunnel in Cloudflare, add a public hostname, and point its service to the application's Podmin DNS name. For example, an application named `web` in the `default` Space on port 8080 is:

```text
http://web.default.space.cluster.local:8080
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

Paste the token at the hidden prompt. Podmin stores it as an encrypted AWS Parameter Store `SecureString`; it is mounted from tmpfs and never written into the Pod manifest.

## Deploy cloudflared

Save the following as `cloudflare-tunnel.yaml`, replacing `<cluster-image>` with the reference printed by `podmin push`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: cloudflare-tunnel
  annotations:
    podmin.dev/aws-parameter-store: tunnel-token
spec:
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

Deploy it in the Space that should run the tunnel connectors:

```sh
podmin deploy cloudflare-tunnel --space default --file cloudflare-tunnel.yaml
```

Every VM in the Space runs one connector for the same tunnel. Cloudflare can route through any healthy connector. No inbound firewall rule or public IPv4 address is required.

To reach an application in another Space, use its full Podmin DNS name in the Cloudflare service configuration:

```text
http://<deployment>.<space>.space.cluster.local:<port>
```
