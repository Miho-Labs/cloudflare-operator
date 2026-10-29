# Architecture

## Components

```
                ┌──────────────────────────────────────────────────────────┐
                │ cloudflare-operator (one Deployment, leader elected)     │
                │                                                          │
   CRs ───────▶ │  controllers ──▶ internal/cloudflare (SDK wrapper) ──────┼──▶ api.cloudflare.com
                │       │                                                  │
                │       └──▶ Secrets / Deployments (cloudflared)           │
                └──────────────────────────────────────────────────────────┘
```

- `api/v1alpha1`: the CRD types. CRDs are generated from them into the Helm chart.
- `internal/cloudflare`: a thin typed wrapper over the official `cloudflare-go/v7` SDK. One file per
  Cloudflare service, with plain Go structs so controllers never see SDK parameter types.
- `internal/cloudflare/fake`: an in-memory Cloudflare API used by all unit tests.
- `internal/controller`: one reconciler per kind plus shared helpers (`common.go`).
- `internal/cloudflared`: builds the connector `Deployment` for a `Tunnel`.
- `cmd/main.go`: the manager. Registers controllers and field indexes.

## Credentials

`CloudflareAccount` is cluster-scoped and references a Secret in any namespace. The
`ClientFactory` reads the Secret, builds an SDK client and caches it keyed by a hash of
token + account ID. Every namespaced resource names its account through `spec.accountRef`.

Resources are re-queued when their account changes (field index on `spec.accountRef.name`) and
accounts are re-verified when their Secret changes (index on the Secret reference). While an account
is `Ready=False`, dependents report `AccountNotReady` and retry every 30 seconds without hitting the
API.

## Reconcile loop

Every controller follows the same shape:

1. Fetch the object. If it is being deleted, run the finalizer path.
2. Add the finalizer if missing.
3. Resolve the account → SDK client.
4. **Observe**: fetch the remote object by the ID stored in `status`. If the ID is unknown (first
   reconcile) or the object vanished, look it up by its natural key (tunnel name, DNS type + name,
   bucket name, Access domain, group/token name) and **adopt** it. Only if nothing exists is a new
   object created.
5. **Diff**: compare the desired state built from `spec` against the remote object, using an
   `Equal` method that ignores server-only fields. Update only when they differ.
6. Write `status`: external IDs, `observedGeneration`, `Ready` and `Synced` conditions.
7. Requeue after `--resync-period` (default 10 minutes) so drift made in the dashboard is corrected.

Errors are classified by `finish()` in `common.go`:

| Error | Condition | Requeue |
|-------|-----------|---------|
| Dependency not ready (account, Tunnel, AccessGroup...) | `Ready=False`, reason `DependencyNotReady` / `AccountNotReady` | 15–30 s, no backoff |
| HTTP 429 | `Synced=False`, `CloudflareAPIError` | 1 min |
| Other API or Kubernetes errors | `Synced=False` | controller-runtime exponential backoff |

## Deletion

Each resource carries the finalizer `cloudflare.miho-labs.dev/finalizer`. On deletion:

- `deletionPolicy: Delete` (default) removes the Cloudflare object, then the finalizer.
- `deletionPolicy: Retain` removes only the Kubernetes object and emits a `Retained` event.

Secrets and Deployments created by the operator have owner references and are garbage collected by
Kubernetes. API tokens created for R2 buckets are always deleted, even when the bucket is retained,
because the token is useless without the Secret.

Cloudflare refuses to delete a non-empty R2 bucket. The `R2Bucket` stays with
`Ready=False/Deleting` and retries every minute until the bucket is emptied or the policy is changed
to `Retain`.

## Tunnels and ingress rules

A `Tunnel` owns the remote tunnel configuration. The `Tunnel` controller:

1. Creates (or adopts) the tunnel with `config_src: cloudflare` so it is remotely managed.
2. Fetches the connector token once and stores it in `<tunnel>-cloudflared` (`TUNNEL_TOKEN`).
3. Renders a `cloudflared` Deployment running `cloudflared tunnel run` with that token, unless
   `spec.cloudflared.enabled: false`.
4. Lists every `TunnelIngress` in the namespace whose `spec.tunnelRef.name` matches (field index),
   builds the ingress rules sorted deterministically (path rules before host-only rules, hostnames
   alphabetically) and appends the catch-all rule. The configuration is pushed only when it differs
   from the remote one, and `cloudflared` picks it up live without restarts.

`TunnelIngress` objects are watched by the `Tunnel` controller, so creating, editing or deleting one
re-reconciles its tunnel. The `TunnelIngress` controller itself only resolves the origin URL, creates
an owned `DNSRecord` (proxied CNAME to `<tunnel-id>.cfargotunnel.com`) and reports `Ready` once the
tunnel status lists its hostname and the record is ready.

## Access

`AccessApplication` policies are **inline**, scoped to the application. Rules reference
`AccessGroup` and `AccessServiceToken` objects in the same namespace by name; the controller resolves
them to Cloudflare IDs from their `status` and waits (`DependencyNotReady`) until they exist.
Applications are re-reconciled whenever a group or service token in the namespace changes.

Access applications and groups are driven through the SDK's generic request path with explicit JSON
structs (`internal/cloudflare/access.go`) because the generated parameter types for inline policies
do not expose the policy fields the operator needs. Everything else uses the typed SDK services.

## R2 credentials

R2 is S3-compatible but has no IAM users. The operator creates an **account-owned API token** whose
policy is limited to the bucket resource `com.cloudflare.edge.r2.bucket.<account>_<jurisdiction>_<bucket>`
with the permission groups for the requested level, then derives the S3 credentials exactly as
Cloudflare documents:

- `AWS_ACCESS_KEY_ID` = token ID
- `AWS_SECRET_ACCESS_KEY` = hex(SHA-256(token value))
- `AWS_ENDPOINT_URL` = `https://<account>.r2.cloudflarestorage.com`

The token value is only returned at creation, so a lost Secret or a permission change results in a
new token (old one deleted).

## Multi-tenancy and RBAC

The operator needs cluster-wide read/write on its CRDs, Secrets, Deployments and Events. The
ClusterRole in the chart is generated from the `+kubebuilder:rbac` markers in the controllers
(`make manifests`), so it never drifts from the code. Restrict the operator to specific namespaces
with `operator.watchNamespaces`.
