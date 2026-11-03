# ManagedWAFRule

Namespaced. Deploys one of Cloudflare's managed WAF rulesets on a zone (the
`http_request_firewall_managed` phase: Security → WAF → Managed rules), with optional overrides.
One object per deployment; usually one per managed ruleset per zone.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: ManagedWAFRule
metadata:
  name: owasp
  namespace: default
spec:
  accountRef:
    name: main
  zone:
    name: example.com
  description: OWASP core ruleset       # natural key for adoption
  ruleset: owasp-core                   # cloudflare-managed | owasp-core | cloudflare-exposed-credentials-check | cloudflare-free-managed
  expression: "true"                    # default: every request
  overrides:
    action: managed_challenge           # action for all rules of the ruleset
    enabled: true
    scoreThreshold: 40                  # owasp-core only: 60 low, 40 medium, 25 high sensitivity
    paranoiaLevel: 2                    # owasp-core only: 1-4 (rules of higher levels are disabled)
    rules:
      - id: 6179ae15870a4bb7b2d480d4843b323c
        action: log
    categories:
      - category: wordpress
        enabled: false
  position:
    index: 1
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `zone.name` / `zone.id` | string | | Zone to protect. |
| `description` | string | | Description of the execute rule; adoption key. |
| `ruleset` | string | | Which managed ruleset to run. Resolved to its ID by listing the zone's managed rulesets. |
| `expression` | string | `true` | Only requests matching this expression are evaluated by the managed ruleset. |
| `enabled` | bool | `true` | Disable without deleting. |
| `overrides.action` | string | | Override the action of every rule (`block`, `challenge`, `managed_challenge`, `js_challenge`, `log`). |
| `overrides.enabled` | bool | | Enable or disable every rule. |
| `overrides.scoreThreshold` | int | | OWASP only. Requests scoring at or above this value trigger the action. Lower is stricter. |
| `overrides.paranoiaLevel` | int | | OWASP only. `1`-`4`; the operator disables the `paranoia-level-N` categories above the chosen level. |
| `overrides.rules[]` | `{id, action, enabled, scoreThreshold}` | | Per-rule overrides. |
| `overrides.categories[]` | `{category, action, enabled}` | | Per-tag overrides (for example `wordpress`, `sqli`, `xss`). |
| `position.index` | int | | Position among the zone's managed-ruleset deployments. |
| `deletionPolicy` | `Delete` \| `Retain` | `Delete` | Whether the deployment is removed on delete. |

The operator token needs **Zone WAF: Edit** on the zone.

## Status

| Field | Description |
|-------|-------------|
| `zoneID`, `rulesetID`, `ruleID`, `ruleVersion` | As for [RateLimitRule](ratelimitrule.md#status). |
| `managedRulesetID` | ID of the Cloudflare managed ruleset being executed. |

## Behaviour

- Managed rulesets are a paid feature: Free zones only have `cloudflare-free-managed` (which is
  deployed by default and cannot be configured). If the chosen ruleset is not available on the zone
  the object stays `Ready=False` with reason `InvalidSpec` and retries hourly.
- The execute rule is adopted by description, so an existing dashboard deployment can be taken
  over by matching its description.
- Overrides are compared as a whole; removing `overrides` from the spec clears them on Cloudflare.
