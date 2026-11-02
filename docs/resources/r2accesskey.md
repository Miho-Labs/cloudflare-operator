# R2AccessKey

Namespaced. Creates an account API token that grants S3-compatible access to one or more R2 buckets
and stores the credentials in a Secret. Unlike `R2Bucket.spec.apiToken`, the key is decoupled from
bucket ownership: several workloads (or namespaces) can each get their own credentials for shared
buckets, with different permission levels, and buckets not managed by the operator can be targeted
by name.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: R2AccessKey
metadata:
  name: reports-reader
  namespace: analytics
spec:
  accountRef:
    name: main
  permission: read-only          # read-only | read-write | admin
  buckets:
    - ref:
        name: reports            # R2Bucket in this namespace
    - name: legacy-exports       # any bucket in the account, by name
      jurisdiction: eu
  secretName: reports-reader-r2  # default <name>-r2-credentials
  tokenName: ""                  # default k8s-<namespace>-<name>
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `permission` | string | `read-write` | `read-only` (*Workers R2 Storage Bucket Item Read*), `read-write` (adds *Bucket Item Write*), or `admin` (*Workers R2 Storage Write* on the whole account; `buckets` is ignored). |
| `buckets[].ref.name` | string | | An `R2Bucket` in the same namespace. The key waits until the bucket exists on Cloudflare. |
| `buckets[].name` | string | | A bucket name. Mutually exclusive with `ref`. |
| `buckets[].jurisdiction` | string | `default` | Jurisdiction of the named bucket. Taken from the `R2Bucket` when `ref` is used. |
| `secretName` | string | `<name>-r2-credentials` | Where the credentials go. |
| `tokenName` | string | `k8s-<namespace>-<name>` | Token name shown in the dashboard. |

At least one bucket is required unless `permission` is `admin`. The operator token needs
**Account API Tokens: Edit**.

## Credentials Secret

| Key | Value |
|-----|-------|
| `AWS_ACCESS_KEY_ID` | API token ID |
| `AWS_SECRET_ACCESS_KEY` | hex(SHA-256(token value)) |
| `AWS_ENDPOINT_URL` | `https://<account-id>.r2.cloudflarestorage.com` |
| `AWS_REGION` | `auto` |
| `BUCKET_NAMES` | comma-separated bucket names (empty for `admin`) |
| `BUCKET_NAME` | the bucket name, only when exactly one bucket is listed |
| `R2_ACCOUNT_ID`, `R2_TOKEN_ID` | account and token IDs |

Buckets in the `eu` or `fedramp` jurisdiction are served from
`https://<account-id>.<jurisdiction>.r2.cloudflarestorage.com`; point the client at that endpoint
for those buckets.

## Status

| Field | Description |
|-------|-------------|
| `tokenID` | API token ID. |
| `buckets` | Bucket names the current token is scoped to. |
| `endpoint` | Default S3 endpoint of the account. |
| `secretName` | The Secret holding the credentials. |

## Behaviour

- The token value is returned only once by Cloudflare. If the Secret is deleted, or the permission
  or bucket set changes, the old token is deleted and a new one created; the Secret is rewritten.
- A referenced `R2Bucket` that is not ready yet puts the key in `DependencyNotReady` and it retries
  every 15 seconds. Changes to `R2Bucket` objects in the namespace re-reconcile the keys.
- Deleting the key always deletes the token. The buckets are never touched.
