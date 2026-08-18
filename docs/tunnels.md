# Ingress Tunnels

Podmin does not include an ingress controller and does not open any inbound ports.

You can expose an application to the internet by deploying a tunnel client as a Pod.

This guide uses a remotely managed [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/setup/).

Using the `podmin install cloudflared` component installation command, it creates outbound-only connections to a remotely managed [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/) - Cloudflare's published application routes can point directly to any Podmin Service, so applications retain complete control over their own hostnames and routing.

This guide publishes the Hello application from [Getting Started](./getting-started.md). Deploy it first so this origin exists:

```text
http://hello.default.svc.cluster.local:8080
```

## Create a Cloudflare Tunnel

You'll need a domain managed by Cloudflare to do this.

Note: This guide is intentionally simple. Cloudflare's [Create a tunnel (dashboard)](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/) guide is the up-to-date, comprehensive source for these dashboard steps.

In the Cloudflare dashboard:

1. Go to **Networking > Tunnels** and select **Create a tunnel**.
2. Name the tunnel and create it.
3. On the connector setup page, __copy only the token__ from the displayed `cloudflared` command. Do not run that command locally.

## Store the Tunnel Token

Store the token at the predefined identity used by Podmin's component:

```sh
podmin secret create tunnel-token \
  --for cloudflared \
  --namespace platform-cloudflared
```

Enter the token at the hidden prompt. Use `--stdin` to read it from standard input or `--file PATH` to read it from a file. The current context's secrets provider is used unless `--provider` overrides it. The resulting provider path is:

```text
/<cluster>/platform-cloudflared/cloudflared/tunnel-token
```

The value is mounted from host tmpfs and never written into a Pod manifest or object storage.

## Install cloudflared

Install one connector on every VM in the selected NodeGroup:

```sh
podmin install cloudflared --nodegroup default
```

Podmin verifies the token exists, automatically downloads and mirrors its pinned multi-platform `cloudflared` image if necessary, and commits the `platform-cloudflared/cloudflared` workload. Re-running the command safely reuses an already mirrored image and commits the same desired configuration. Use `--provider` here too if the token was created with a provider other than the context default.

Wait for the tunnel to show **Healthy** in Cloudflare before continuing. Every VM in the NodeGroup is a connector for the same tunnel, and Cloudflare can route through any healthy connector. See Cloudflare's [replica and high-availability documentation](https://developers.cloudflare.com/tunnel/configuration/#replicas-and-high-availability) for availability details.

## Publish Hello

In the Cloudflare dashboard:

1. Open the tunnel's **Routes** tab.
2. Add a **Published application** route and choose its public hostname, such as `hello.example.com`.
3. Set **Service URL** to:

   ```text
   http://hello.default.svc.cluster.local:8080
   ```

4. Save the route, then open the public hostname. It should display `Hello, World!`.

Follow Cloudflare's [published application instructions](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/#2a-publish-an-application) and [origin parameters reference](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/origin-parameters/) for current routing options, TLS settings, timeouts, and other origin behavior.

No ingress controller or shared ingress proxy is required. Add more published application routes directly to each Service:

```text
http://<service>.<namespace>.svc.cluster.local:<port>
```

If an application needs host- or path-based routing behind one origin, deploy your reverse proxy of choice, such as Traefik, as an ordinary Podmin workload and point Cloudflare at its Service.

## Next Steps

- [CLI Reference](./cli.md) details every command and argument for the Podmin CLI.
- [Custom Workloads](./workloads.md) covers custom DaemonSet manifests, Services, secrets, and multi-platform images.
- [GitHub Actions](./github-actions.md) automates cluster setup and application deployment.
