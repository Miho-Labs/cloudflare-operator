# CloudflareAccount

Cluster-scoped. Holds the credentials every other resource uses through `spec.accountRef`.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: CloudflareAccount
metadata:
  name: main
spec:
  accountID: 0123456789abcdef0123456789abcdef
  apiTokenSecretRef:
    name: cloudflare-api-token
    namespace: cloudflare-operator
    key: apiToken
```

## Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `accountID` | string | yes | The Cloudflare Account ID (32 hex characters). See [Getting started](../getting-started.md#1-find-your-account-id). |
| `apiTokenSecretRef.name` | string | yes | Secret holding the API token. |
| `apiTokenSecretRef.namespace` | string | yes | Namespace of the Secret. |
| `apiTokenSecretRef.key` | string | no | Key inside the Secret. Default `apiToken`. |
| `apiBaseURL` | string | no | Override the API endpoint. Only for testing against a fake API. |

## Status

| Field | Description |
|-------|-------------|
| `accountName` | Name reported by Cloudflare, proof the token can read the account. |
| `tokenStatus` | `active`, `disabled`, `expired`, or `unknown` when the token cannot be introspected. |
| `lastVerified` | Time of the last successful verification. |
| `conditions` | `Ready` is `True` once `GET /accounts/{id}` succeeds. |

The account is re-verified every 30 minutes and whenever its Secret changes. While it is not ready,
dependent resources report `AccountNotReady` and do not call the API.

## Required token permissions

See the table in [Getting started](../getting-started.md#2-create-an-api-token). At minimum the
token needs **Account Settings: Read** for verification to succeed.

## Multiple accounts

Create one `CloudflareAccount` per Cloudflare account or per team token. Resources pick one with
`spec.accountRef.name`. There is no default account; the reference is always explicit.
