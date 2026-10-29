# TunnelIngress

Namespaced. Publishes one hostname (an "app") through a [Tunnel](tunnel.md) in the same namespace.
The rule is pushed to the tunnel's remote configuration and, by default, a proxied CNAME record is
created for the hostname.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: TunnelIngress
metadata:
  name: app
  namespace: default
spec:
  tunnelRef:
    name: edge
  hostname: app.example.com
  path: ""                      # optional regex, e.g. ^/api/
  service:
    name: app                   # Service in this namespace ...
    port: 8080
    scheme: http
    # url: https://api.other.svc.cluster.local:8443   # ... or an explicit origin URL
  originRequest:
    noTLSVerify: true
    httpHostHeader: app.internal
  dns:
    enabled: true
    zone:                       # optional; by default the longest matching zone in the account
      name: example.com
    comment: ""
    deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tunnelRef.name` | string | | Tunnel in the same namespace. |
| `hostname` | string | | Public hostname. Must be in a zone of the account for DNS to work. |
| `path` | string | | Regular expression on the request path. Rules with a path are evaluated before host-only rules for the same hostname. |
| `service.name` + `service.port` + `service.scheme` | | `scheme: http` | Kubernetes Service in this namespace, rendered as `scheme://name.namespace.svc.cluster.local:port`. |
| `service.url` | string | | Explicit origin: `http://`, `https://`, `tcp://`, `ssh://`, `rdp://`, `smb://`, `unix://` or `http_status:<code>`. Mutually exclusive with `service.name`. |
| `originRequest` | object | | Per-hostname overrides of the Tunnel's `originRequest`. Same fields. |
| `dns.enabled` | bool | `true` | Create the CNAME `hostname → <tunnel-id>.cfargotunnel.com` (proxied). |
| `dns.zone` | `{name}` or `{id}` | auto | Zone for the record. Auto-detection tries `example.com` for `a.b.example.com`, then `b.example.com`, and so on. |
| `dns.comment` | string | generated | Comment on the DNS record. |
| `dns.deletionPolicy` | `Delete`, `Retain` | `Delete` | Passed to the owned DNSRecord. |

## Status

| Field | Description |
|-------|-------------|
| `serviceURL` | The resolved origin URL pushed to the tunnel. |
| `tunnelID` | ID of the tunnel the rule is attached to. |
| `dnsRecordName` | The owned `DNSRecord` (`<name>-cname`), empty when DNS is disabled. |
| `conditions` | `Ready` once the Tunnel reports the hostname in `status.syncedHostnames` and the DNSRecord is ready. |

## Behaviour

- Several `TunnelIngress` objects can share a hostname with different paths. Only one of them should
  create the DNS record; set `dns.enabled: false` on the others (see `examples/03-tunnel-with-apps.yaml`).
- The DNSRecord is owned by the TunnelIngress and garbage collected with it; its own finalizer then
  removes the record from Cloudflare.
- Deleting a TunnelIngress re-reconciles the Tunnel, which drops the rule from the remote
  configuration. No finalizer is needed on the TunnelIngress itself.
- Non-HTTP origins (`ssh://`, `tcp://`, `rdp://`) are reachable from clients through
  `cloudflared access` or the WARP client, as with any Cloudflare Tunnel.
