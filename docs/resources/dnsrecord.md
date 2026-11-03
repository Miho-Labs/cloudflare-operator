# DNSRecord

Namespaced. Manages one DNS record in a zone of the account. Independent of tunnels; `TunnelIngress`
creates DNSRecords for its hostnames using the same controller.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: DNSRecord
metadata:
  name: www
  namespace: default
spec:
  accountRef:
    name: main
  zone:
    name: example.com        # or id: <zone id>
  type: A
  name: www.example.com      # full name; "@" is not expanded, use the zone apex
  content: 203.0.113.10
  ttl: 1                     # 1 = automatic; ignored when proxied
  proxied: true
  priority: 10               # MX / SRV only
  comment: managed by cloudflare-operator
  tags: []
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `zone.name` / `zone.id` | string | | Exactly one of them. Names are resolved once and cached. |
| `type` | `A`, `AAAA`, `CNAME`, `TXT`, `MX`, `NS`, `SRV`, `CAA`, `PTR` | | Record type. |
| `name` | string | | Fully qualified record name. |
| `content` | string | | Record value. |
| `ttl` | int | `1` | Seconds; `1` means automatic. Proxied records are always automatic. |
| `proxied` | bool | `false` | Route through Cloudflare (A, AAAA, CNAME only). |
| `priority` | int | | MX and SRV priority. |
| `comment` | string | | Comment shown in the dashboard, at most 100 characters. |
| `tags` | []string | | Record tags (plan permitting). |
| `deletionPolicy` | `Delete`, `Retain` | `Delete` | Whether to delete the record from Cloudflare. |

## Status

| Field | Description |
|-------|-------------|
| `recordID`, `zoneID` | Identity on Cloudflare. |
| `fqdn` | Name as returned by Cloudflare. |

## Behaviour

- **Adoption**: before creating, the controller looks for a record in the zone with the same type,
  name **and content** and adopts it, so records created by hand can be imported safely. Several
  records of one type at one name (TXT at the apex, MX, NS) are therefore handled individually: a
  DNSRecord whose content matches none of them creates a new record next to them.
  Two extra rules apply to A/AAAA/CNAME, which cannot coexist with a CNAME at one name: a desired
  CNAME takes over whatever A/AAAA/CNAME exists there, and a desired A/AAAA takes over an existing
  CNAME or a *lone* existing record of its type with different content (when several exist, a new
  one is created).
- **Duplicates**: if an update is rejected because an identical record already exists (error
  81058), the object is re-pointed at that record and the previously referenced one is left alone.
- **Drift**: every resync the record is compared field by field (type, name, content, proxied, TTL
  unless proxied, priority, comment, tags) and overwritten when it differs. CNAME/MX/NS targets are
  compared case-insensitively and ignoring a trailing dot.
- Changing `type` or `name` in the spec updates the existing record in place (same ID); Cloudflare
  allows that.
- If the record is deleted in the dashboard, the next resync recreates it and emits a `Failed`
  warning event.
