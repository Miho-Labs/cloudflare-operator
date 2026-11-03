# IPAccessRule

Namespaced. Manages one IP access rule (Security → WAF → Tools → IP Access Rules), either for a
single zone or for the whole account. Rules match an IP, a CIDR range, an ASN or a country and
apply an action before any other security product.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: IPAccessRule
metadata:
  name: block-scanner
  namespace: default
spec:
  accountRef:
    name: main
  scope: zone                   # zone (default) | account
  zone:
    name: example.com           # required for scope: zone
  mode: block                   # block | challenge | managed_challenge | js_challenge | whitelist
  target: ip                    # ip | ip_range | asn | country
  value: 198.51.100.7           # 203.0.113.0/24, AS64496, KP, ...
  notes: abusive scanner
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `scope` | `zone` \| `account` | `zone` | Zone-level rule or account-wide rule (applies to every zone in the account). |
| `zone.name` / `zone.id` | string | | Required when `scope` is `zone`. |
| `mode` | string | | Action. `whitelist` allows the source through all other security checks. |
| `target` | string | | `ip` (single IPv4/IPv6), `ip_range` (IPv4 `/16`-`/24` or IPv6 `/32`-`/64`), `asn` (`AS1234`), `country` (ISO 3166-1 alpha-2). |
| `value` | string | | The address, range, ASN or country code. |
| `notes` | string | | Free text shown in the dashboard. |
| `deletionPolicy` | `Delete` \| `Retain` | `Delete` | Whether the rule is removed on delete. |

Token permissions: **Firewall Services: Edit** on the zone for `scope: zone`, **Account Firewall
Access Rule: Edit** for `scope: account`.

## Status

| Field | Description |
|-------|-------------|
| `ruleID` | Cloudflare rule ID. |
| `zoneID` | Resolved zone ID (zone scope only). |

## Behaviour

- Adoption key is (`target`, `value`) within the scope: an existing rule for the same source is
  adopted and its mode and notes are updated.
- Cloudflare does not allow changing the target or value of a rule: when they change the old rule
  is deleted and a new one created (the rule ID changes).
- Cloudflare rejects a duplicate (`target`, `value`) in the same scope; the operator avoids this by
  adopting, but a rule created in the dashboard between two reconciles surfaces the error in the
  `Synced` condition and is adopted on the next pass.
- Country rules and `whitelist` on the account scope require a paid plan for some targets; the
  API error is reported in the condition.
