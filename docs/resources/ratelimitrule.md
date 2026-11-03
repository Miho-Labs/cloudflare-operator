# RateLimitRule

Namespaced. Manages one rate limiting rule in a zone (the `http_ratelimit` ruleset phase). Each
object is one rule; rules created in the dashboard are left alone, so the operator can coexist with
manually managed rules in the same zone.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: RateLimitRule
metadata:
  name: login-throttle
  namespace: default
spec:
  accountRef:
    name: main
  zone:
    name: example.com               # or id: <zone id>
  description: login throttle       # unique per zone; used to adopt an existing rule
  expression: '(http.request.uri.path eq "/api/login" and http.request.method eq "POST")'
  action: block                     # block | managed_challenge | challenge | js_challenge | log
  blockResponse:                    # optional custom response for action: block
    statusCode: 429
    contentType: application/json
    body: '{"error":"too many requests"}'
  rateLimit:
    characteristics: ["ip.src", "cf.colo.id"]
    period: 60                      # seconds: 10 | 60 | 120 | 300 | 600 | 3600
    requestsPerPeriod: 20
    mitigationTimeout: 600          # seconds the action applies once the limit is hit
    countingExpression: ""          # optional: count only matching requests (e.g. responses with status 401)
    requestsToOrigin: false         # count only requests that reach the origin
  position:
    index: 1                        # optional 1-based position in the ruleset; omitted = append
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `zone.name` / `zone.id` | string | | Zone the rule belongs to. |
| `description` | string | | Rule description. Natural key: an existing rule with the same description is adopted. |
| `expression` | string | | [Rules language](https://developers.cloudflare.com/ruleset-engine/rules-language/) filter selecting the requests to count. |
| `enabled` | bool | `true` | Disable without deleting. |
| `action` | string | `block` | What to do once the limit is exceeded. |
| `blockResponse` | object | | Custom status (`400`-`499`), `contentType` (default `text/plain`) and `body` for `action: block`. |
| `rateLimit.characteristics` | []string | `["ip.src", "cf.colo.id"]` | Dimensions requests are counted by. `cf.colo.id` is required by Cloudflare on all plans except Enterprise. |
| `rateLimit.period` | int | `10` | Counting window in seconds. Free and Pro plans allow 10 s only; Business adds 60 s; Enterprise all values. |
| `rateLimit.requestsPerPeriod` | int | | Requests allowed per period (per characteristic tuple). |
| `rateLimit.mitigationTimeout` | int | = `period` | How long the action applies after the limit is reached. |
| `rateLimit.countingExpression` | string | | Count only requests matching this expression (may reference response fields). |
| `rateLimit.requestsToOrigin` | bool | `false` | Count only requests that were not served from cache. |
| `position.index` | int | | 1-based position in the phase ruleset. Rules are evaluated in order. |
| `deletionPolicy` | `Delete` \| `Retain` | `Delete` | Whether the rule is removed from Cloudflare when the object is deleted. |

The operator token needs **Zone WAF: Edit** on the zone.

## Status

| Field | Description |
|-------|-------------|
| `zoneID` | Resolved zone ID. |
| `rulesetID` | The zone's `http_ratelimit` entrypoint ruleset (created on first use). |
| `ruleID` | ID of the rule inside that ruleset. |
| `ruleVersion` | Version reported by Cloudflare after the last update. |

## Behaviour

- The zone's phase entrypoint ruleset is created if it does not exist. Other rules in it are never
  touched.
- Drift (someone edits the rule in the dashboard) is corrected on the next resync. Edits to rules
  the operator does not own are kept.
- If the rule disappears from the ruleset it is recreated and an event is recorded.
- `position` is enforced on every reconcile; without it the rule stays where it was added.
- Cloudflare validates expressions and plan limits on write. The error (for example
  `period must be 10 on this plan`) is surfaced in the `Synced` condition.
