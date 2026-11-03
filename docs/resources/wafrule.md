# WAFRule

Namespaced. Manages one custom WAF rule in a zone (the `http_request_firewall_custom` phase: Security
→ WAF → Custom rules in the dashboard). One object per rule; dashboard rules coexist.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: WAFRule
metadata:
  name: admin-geo-fence
  namespace: default
spec:
  accountRef:
    name: main
  zone:
    name: example.com
  description: admin only from PL     # unique per zone; natural key for adoption
  expression: '(http.request.uri.path contains "/admin" and not ip.geoip.country eq "PL")'
  action: block                       # block | challenge | managed_challenge | js_challenge | log | skip
  blockResponse:                      # optional, action: block only
    statusCode: 403
    contentType: text/plain
    body: forbidden
  enabled: true
  position:
    index: 1
  deletionPolicy: Delete
---
# A skip rule exempts matching requests from other security products.
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: WAFRule
metadata:
  name: allow-monitoring
  namespace: default
spec:
  accountRef:
    name: main
  zone:
    name: example.com
  description: skip WAF for uptime checks
  expression: '(http.user_agent contains "UptimeRobot")'
  action: skip
  skip:
    ruleset: current                  # current | all: skip remaining custom rules
    products: ["waf", "rateLimit"]    # zoneLockdown | uaBlock | bic | hot | securityLevel | rateLimit | waf
    phases: []                        # e.g. ["http_ratelimit", "http_request_firewall_managed"]
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `zone.name` / `zone.id` | string | | Zone the rule belongs to. |
| `description` | string | | Rule description and adoption key. |
| `expression` | string | | [Rules language](https://developers.cloudflare.com/ruleset-engine/rules-language/) expression. |
| `enabled` | bool | `true` | Disable without deleting. |
| `action` | string | `block` | Rule action. |
| `blockResponse` | object | | Custom response for `block` (status `400`-`499`, `contentType`, `body`). |
| `skip.ruleset` | `current` \| `all` | | For `action: skip`: skip the rest of the custom ruleset (`current`) or all remaining rules (`all`). |
| `skip.phases` | []string | | Phases to skip entirely (for example `http_ratelimit`). |
| `skip.products` | []string | | Legacy security products to skip. |
| `position.index` | int | | 1-based position in the custom ruleset. |
| `deletionPolicy` | `Delete` \| `Retain` | `Delete` | Whether the rule is removed from Cloudflare on delete. |

With `action: skip` and no `skip` block, `ruleset: current` is used. The operator token needs
**Zone WAF: Edit** on the zone.

## Status

Same as [RateLimitRule](ratelimitrule.md): `zoneID`, `rulesetID`, `ruleID`, `ruleVersion`.

## Behaviour

Identical to [RateLimitRule](ratelimitrule.md#behaviour): the custom-rules entrypoint is created on
first use, the rule is adopted by description, drift is corrected, position is enforced, and
deletion removes only this rule. Free zones allow 5 custom rules, Pro 20, Business 100.
