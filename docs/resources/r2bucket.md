# R2Bucket

Namespaced. Manages an R2 bucket and, optionally, an account-owned API token scoped to that bucket
whose S3-compatible credentials are written to a Secret.

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: R2Bucket
metadata:
  name: uploads
  namespace: default
spec:
  accountRef:
    name: main
  name: acme-uploads          # bucket name, defaults to metadata.name
  locationHint: weur          # apac | eeur | enam | weur | wnam | oc
  storageClass: Standard      # Standard | InfrequentAccess
  jurisdiction: default       # default | eu | fedramp
  apiToken:
    enabled: true
    permission: read-write    # read-only | read-write | admin
    secretName: uploads-r2    # default <name>-r2-credentials
    tokenName: ""             # default k8s-<namespace>-<name>
  deletionPolicy: Delete
```

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `accountRef.name` | string | | `CloudflareAccount` to use. |
| `name` | string | `metadata.name` | Bucket name (3–63 chars, lowercase letters, digits, hyphens). An existing bucket is adopted. |
| `locationHint` | string | | Region hint applied at creation only. |
| `storageClass` | string | | Default storage class for new objects, creation only. |
| `jurisdiction` | string | `default` | Data jurisdiction, creation only. Changes the S3 endpoint. |
| `apiToken.enabled` | bool | `false` | Create a token and the credentials Secret. Needs **Account API Tokens: Edit** on the operator token. |
| `apiToken.permission` | string | `read-write` | `read-only`: *Workers R2 Storage Bucket Item Read*. `read-write`: adds *Bucket Item Write*. `admin`: *Workers R2 Storage Write* on the whole account (can create/delete buckets). |
| `apiToken.secretName` | string | `<name>-r2-credentials` | Where the credentials go. |
| `apiToken.tokenName` | string | `k8s-<namespace>-<name>` | Token name shown in the dashboard. |
| `deletionPolicy` | `Delete`, `Retain` | `Delete` | Delete the bucket with the object. The token is always deleted. |

## Credentials Secret

| Key | Value |
|-----|-------|
| `AWS_ACCESS_KEY_ID` | API token ID |
| `AWS_SECRET_ACCESS_KEY` | hex(SHA-256(token value)) as documented by Cloudflare |
| `AWS_ENDPOINT_URL` | `https://<account-id>.r2.cloudflarestorage.com` (or `<account-id>.<jurisdiction>.r2...`) |
| `AWS_REGION` | `auto` |
| `BUCKET_NAME` | bucket name |
| `R2_ACCOUNT_ID` | account ID |
| `R2_TOKEN_ID` | API token ID |

Any S3 SDK works with `envFrom: [{secretRef: {name: <secret>}}]`. For the AWS CLI also set
`AWS_REQUEST_CHECKSUM_CALCULATION=when_required` on recent versions.

## Status

| Field | Description |
|-------|-------------|
| `bucketName` | Name on Cloudflare. |
| `endpoint` | S3 endpoint for this account/jurisdiction. |
| `tokenID` | ID of the API token. |
| `credentialsSecretName` | The Secret holding the credentials. |

## Behaviour

- The token value is returned only once by Cloudflare. If the Secret is deleted or the requested
  permission changes, the old token is deleted and a new one created; the Secret is rewritten.
- Disabling `apiToken.enabled` deletes the token and leaves the Secret to garbage collection.
- Cloudflare refuses to delete non-empty buckets. The object stays in `Deleting` with a clear
  condition message and retries every minute. Empty the bucket or switch to `deletionPolicy: Retain`.
- Bucket settings (`locationHint`, `storageClass`, `jurisdiction`) are applied at creation and not
  reconciled afterwards, matching what the API allows.
