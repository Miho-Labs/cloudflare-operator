# AccessGroup

Namespaced. Manages a reusable Zero Trust Access group. Reference it from
[AccessApplication](accessapplication.md) policies with `groups: [{name: <AccessGroup name>}]`.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: AccessGroup
metadata:
  name: engineering
  namespace: default
spec:
  accountRef:
    name: main
  name: Engineering             # defaults to metadata.name
  include:
    - emailDomains: [example.com]
    - githubOrganizations:
        - name: acme
          identityProviderID: 8a1b...   # GitHub IdP configured in Zero Trust
          teams: [platform]
  exclude:
    - emails: [contractor@example.com]
  require:
    - countries: [PL, DE]
  deletionPolicy: Delete
```

Rule syntax is the same as for applications; see the [rules table](accessapplication.md#rules).

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `name` | string | `metadata.name` | Group name on Cloudflare; the natural key for adoption. |
| `include` | []rule | | At least one. |
| `exclude`, `require` | []rule | | Optional. |
| `deletionPolicy` | `Delete`, `Retain` | `Delete` | Whether to delete the group from Cloudflare. |

## Status

`groupID` is the Cloudflare group ID; `Ready` is `True` once the group matches the spec.

Groups can themselves reference other groups through `groups: [{name: ...}]`; the controller
resolves them the same way and waits for them to be ready.
