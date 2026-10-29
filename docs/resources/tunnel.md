# Tunnel

Namespaced. Manages a remotely-configured Cloudflare Tunnel, the Secret with its connector token,
and by default a `cloudflared` Deployment that runs the connector in-cluster. Public hostnames are
attached with [TunnelIngress](tunnelingress.md).

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: Tunnel
metadata:
  name: edge
  namespace: default
spec:
  accountRef:
    name: main
  name: edge-prod            # optional, defaults to metadata.name
  cloudflared:
    enabled: true
    replicas: 2
    image: cloudflare/cloudflared:2025.10.0
    protocol: auto
    resources:
      requests: {cpu: 50m, memory: 64Mi}
      limits: {memory: 256Mi}
    nodeSelector: {}
    tolerations: []
    podAnnotations: {}
    priorityClassName: ""
    extraArgs: []
  originRequest:             # tunnel-wide defaults for every hostname
    connectTimeout: 10
    noTLSVerify: false
  catchAllService: http_status:404
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `name` | string | `metadata.name` | Tunnel name on Cloudflare. An existing remotely-managed tunnel with that name is adopted. |
| `cloudflared.enabled` | bool | `true` | Run `cloudflared` pods. When `false` the operator still creates the tunnel and the token Secret; run the connector yourself. |
| `cloudflared.replicas` | int | `2` | Connector replicas. Each opens four connections to Cloudflare's edge. |
| `cloudflared.image` | string | operator flag `--cloudflared-image` | Connector image. |
| `cloudflared.protocol` | `auto`, `quic`, `http2` | `auto` | Transport between cloudflared and the edge. |
| `cloudflared.extraArgs` | []string | | Extra arguments inserted before `run`. |
| `cloudflared.resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, `priorityClassName` | | | Standard pod scheduling fields. Affinity defaults to soft anti-affinity across nodes. |
| `originRequest` | object | | Tunnel-wide origin settings. Fields: `noTLSVerify`, `httpHostHeader`, `originServerName`, `connectTimeout`, `tlsTimeout`, `tcpKeepAlive`, `keepAliveConnections`, `keepAliveTimeout`, `disableChunkedEncoding`, `http2Origin`, `noHappyEyeballs`, `proxyType`, `caPool`. A `TunnelIngress` can override any of them per hostname. |
| `catchAllService` | string | `http_status:404` | What requests that match no `TunnelIngress` get. |
| `deletionPolicy` | `Delete`, `Retain` | `Delete` | Whether the tunnel is deleted from Cloudflare with the object. |

## Status

| Field | Description |
|-------|-------------|
| `tunnelID`, `tunnelName` | Identity on Cloudflare. |
| `tokenSecretName` | Secret with `TUNNEL_TOKEN` and `TUNNEL_ID`. Use it to run cloudflared elsewhere: `cloudflared tunnel run --token $TUNNEL_TOKEN`. |
| `connectorStatus` | `inactive`, `degraded`, `healthy` or `down`, as reported by Cloudflare. |
| `ingressCount`, `syncedHostnames` | Rules currently in the remote configuration (`hostname` + `path`). |
| `configVersion` | Version of the remote configuration last observed or pushed. |

`kubectl get tunnels` shows the tunnel ID, connector status, ingress count and readiness.

## Behaviour

- The tunnel is created with `config_src: cloudflare`. Locally-managed tunnels (configured with a
  `config.yml` on the host) with the same name are **not** adopted; the resource reports an error.
- The connector token is fetched once and stored in `<name>-cloudflared`. It never changes for the
  life of the tunnel.
- The Deployment `<name>-cloudflared` is owned by the Tunnel and garbage collected with it. It runs
  as a non-root user with a read-only filesystem, exposes `/ready` and `/metrics` on port 2000, and
  uses a rolling update with `maxUnavailable: 0`.
- Ingress rules are assembled from every `TunnelIngress` in the same namespace that references the
  tunnel, ordered deterministically, and pushed only when they differ from the remote configuration.
  `cloudflared` reloads remote configuration live.
- On deletion with `deletionPolicy: Delete`, stale connections are cleaned up and the tunnel is
  deleted. The CNAME records created by `TunnelIngress` objects are deleted by their own owners.

## Events

`Created`, `Adopted`, `Updated` (configuration pushed, deployment changed), `Deleted`, `Retained`,
`Failed` (tunnel disappeared and was recreated).
