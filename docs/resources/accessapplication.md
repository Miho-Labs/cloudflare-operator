# AccessApplication

Namespaced. Manages a Zero Trust Access **self-hosted application** with inline policies. Policies
can reference [AccessGroup](accessgroup.md) and [AccessServiceToken](accessservicetoken.md) objects
in the same namespace by name.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: AccessApplication
metadata:
  name: app
  namespace: default
spec:
  accountRef:
    name: main
  name: Internal App              # defaults to metadata.name
  domain: app.example.com         # may include a path: app.example.com/admin
  selfHostedDomains: []           # extra domains protected by the same app
  sessionDuration: 24h
  allowedIdps: []                 # identity provider IDs; empty = all
  autoRedirectToIdentity: false
  appLauncherVisible: true
  skipInterstitial: false
  serviceAuth401Redirect: false
  httpOnlyCookieAttribute: true
  customDenyMessage: ""
  customDenyURL: ""
  logoURL: ""
  corsHeaders:
    allowAllOrigins: false
    allowedOrigins: ["https://app.example.com"]
    allowedMethods: ["GET", "POST"]
    allowCredentials: true
    maxAge: 600
  policies:
    - name: ci
      decision: non_identity
      include:
        - serviceTokens: [{name: ci}]
    - name: staff
      decision: allow
      sessionDuration: 8h
      include:
        - groups: [{name: engineering}]
        - emails: [ops@example.com]
      exclude:
        - countries: [RU]
      require:
        - ips: [198.51.100.0/24]
  deletionPolicy: Delete
```

## Rules

`include`, `exclude` and `require` are lists of rule objects. Each field of a rule object expands to
one Cloudflare rule, and rules within the same list are OR-ed (`include`: at least one must match,
`exclude`: none may match, `require`: all must match).

| Field | Cloudflare rule |
|-------|-----------------|
| `emails: [a@x.com]` | `email` |
| `emailDomains: [x.com]` | `email_domain` |
| `everyone: true` | `everyone` |
| `ips: [10.0.0.0/8]` | `ip` |
| `countries: [PL]` | `geo` |
| `loginMethods: [<idp id>]` | `login_method` |
| `groups: [{name: g}]` or `[{id: <uuid>}]` | `group` (name resolves an `AccessGroup` in this namespace) |
| `serviceTokens: [{name: t}]` or `[{id: <uuid>}]` | `service_token` (name resolves an `AccessServiceToken`) |
| `anyValidServiceToken: true` | `any_valid_service_token` |
| `githubOrganizations: [{name, identityProviderID, teams}]` | `github-organization`, one rule per team |

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `domain` | string | | Protected domain, optionally with a path. Used as the natural key for adoption. |
| `policies[]` | list | | At least one. Evaluated in order (`precedence` = list index). `decision` is `allow`, `deny`, `bypass` or `non_identity`. `purposeJustificationRequired`/`Prompt` are supported. |
| other fields | | | Map 1:1 to the Access application settings of the same name. |
| `deletionPolicy` | `Delete`, `Retain` | `Delete` | Whether to delete the application from Cloudflare. |

## Status

| Field | Description |
|-------|-------------|
| `applicationID` | Application ID. |
| `aud` | Audience tag, needed to validate the `CF_Authorization` JWT in your app. |
| `policyCount` | Number of attached policies. |

## Behaviour

- Until every referenced group and service token has an ID in its status, the application reports
  `Ready=False/DependencyNotReady` and retries every 15 seconds. Nothing is created on Cloudflare
  with half-resolved policies.
- An existing application for the same `domain` is adopted and brought in line with the spec,
  including replacing its policies.
- Changing a group or service token in the namespace re-reconciles every application in it.
- Reusable (account-level) policies are not managed; use inline policies. The application's own
  policies are replaced wholesale on every update.
