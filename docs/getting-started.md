# Getting started

This guide takes you from nothing to a working `CloudflareAccount` and a first tunnel.

## 1. Find your Account ID

Cloudflare calls the organisation-level identifier the **Account ID** (some people call it the
"org id"). It is a 32-character hexadecimal string.

1. Log in to <https://dash.cloudflare.com>.
2. Click any domain (zone) in the account, or open **Workers & Pages**.
3. The **Account ID** is shown in the right-hand sidebar under *API*; click **Copy**.

It is also the first path segment of most dashboard URLs:
`https://dash.cloudflare.com/<account-id>/...`.

If you have access to several accounts, pick the one that owns the zones and the Zero Trust
organisation you want the operator to manage. Each `CloudflareAccount` resource maps to exactly one
account; create several if you need more.

## 2. Create an API token

The operator authenticates with a scoped **API token**, never the Global API Key.

1. Open <https://dash.cloudflare.com/profile/api-tokens> (My Profile → API Tokens).
2. Click **Create Token** → **Create Custom Token** (at the bottom).
3. Give it a name such as `cloudflare-operator`.
4. Add the permissions below. Only add what you need: a token without the R2 permissions can still
   manage tunnels and DNS; the matching resources will simply fail with `403` until you add them.

| Scope | Permission | Access | Needed by |
|-------|------------|--------|-----------|
| Account | Account Settings | Read | `CloudflareAccount` verification |
| Account | Cloudflare Tunnel | Edit | `Tunnel`, `TunnelIngress` |
| Account | Access: Apps and Policies | Edit | `AccessApplication`, `AccessGroup` |
| Account | Access: Service Tokens | Edit | `AccessServiceToken` |
| Account | Workers R2 Storage | Edit | `R2Bucket` |
| Account | Account API Tokens | Edit | `R2Bucket` with `apiToken.enabled` (creates bucket-scoped tokens) |
| Zone | Zone | Read | `DNSRecord`, `TunnelIngress` (zone lookup) |
| Zone | DNS | Edit | `DNSRecord`, `TunnelIngress` |

5. Under **Account Resources** choose *Include → the account from step 1*. Under **Zone
   Resources** choose *Include → All zones from an account → that account* (or list the specific
   zones).
6. Optionally restrict **Client IP Address Filtering** to your cluster's egress IPs and set a TTL.
7. Click **Continue to summary** → **Create Token** and copy the token. It is shown only once.

You can verify the token before using it:

```bash
curl -s https://api.cloudflare.com/client/v4/user/tokens/verify \
  -H "Authorization: Bearer $CF_API_TOKEN" | jq .result.status
# "active"
```

> **Account-owned tokens.** If your organisation uses account-owned API tokens (Manage Account →
> Account API Tokens) instead of user tokens, they work the same way. Give them the same
> permissions. The operator verifies the account with `GET /accounts/{id}`, which works for both.

## 3. Install the operator

```bash
helm upgrade --install cloudflare-operator ./charts/cloudflare-operator \
  --namespace cloudflare-operator --create-namespace
kubectl -n cloudflare-operator rollout status deploy/cloudflare-operator
```

Useful values (see `charts/cloudflare-operator/values.yaml` for all of them):

| Value | Default | Meaning |
|-------|---------|---------|
| `operator.resyncPeriod` | `10m` | How often resources are compared with Cloudflare and drift corrected. |
| `operator.cloudflaredImage` | `cloudflare/cloudflared:2025.10.0` | Default connector image for Tunnels. |
| `operator.watchNamespaces` | `""` (all) | Comma-separated namespaces to watch. |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor`. |

The chart installs the CRDs from its `crds/` directory. Helm does not upgrade CRDs on
`helm upgrade`; after upgrading the chart run:

```bash
kubectl apply --server-side -f charts/cloudflare-operator/crds/
```

## 4. Register the account

Store the token in a Secret and point a cluster-scoped `CloudflareAccount` at it:

```bash
kubectl -n cloudflare-operator create secret generic cloudflare-api-token \
  --from-literal=apiToken='<token from step 2>'
```

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: CloudflareAccount
metadata:
  name: main
spec:
  accountID: 0123456789abcdef0123456789abcdef   # from step 1
  apiTokenSecretRef:
    name: cloudflare-api-token
    namespace: cloudflare-operator
    key: apiToken                                 # default
```

```bash
kubectl apply -f account.yaml
kubectl get cloudflareaccount main
# NAME   ACCOUNT ID                         ACCOUNT    READY   AGE
# main   0123456789abcdef0123456789abcdef   Acme Inc   True    5s
```

If `READY` is `False`, `kubectl describe cloudflareaccount main` shows why (wrong account ID,
rejected token, missing Secret key). Dependent resources wait with `Ready=False/AccountNotReady`
until the account verifies.

The Secret can live in any namespace; the operator reads it with its own service account. Teams can
have their own `CloudflareAccount` objects with differently scoped tokens, and resources choose one
with `spec.accountRef.name`.

## 5. Create your first resources

Apply the examples in order and watch them become ready:

```bash
kubectl apply -f examples/03-tunnel-with-apps.yaml
kubectl get tunnels,tunnelingresses,dnsrecords -n default
```

```
NAME                                   TUNNEL ID                              CONNECTOR   INGRESSES   READY
tunnel.cloudflare.miho-labs.dev/edge   9a8b7c6d-...                           healthy     3           True

NAME                                        TUNNEL   HOSTNAME          SERVICE                                     READY
tunnelingress.cloudflare.miho-labs.dev/app  edge     app.example.com   http://app.default.svc.cluster.local:8080   True
```

`kubectl get pods -l app.kubernetes.io/name=cloudflared` shows the connector pods. Once they report
`healthy`, `https://app.example.com` is live.

Next:

- Protect it with Zero Trust: `examples/05-access.yaml`.
- Give a workload an R2 bucket and S3 credentials: `examples/04-r2-bucket.yaml`.
- Manage plain DNS records: `examples/02-dns-record.yaml`.

## 6. Uninstall

Delete the custom resources first so the operator can clean up on Cloudflare (or set
`deletionPolicy: Retain` on the ones you want to keep), then uninstall the chart:

```bash
kubectl delete tunnelingresses,tunnels,dnsrecords,r2buckets,accessapplications,accessgroups,accessservicetokens --all -A
helm uninstall cloudflare-operator -n cloudflare-operator
kubectl get crd -o name | grep cloudflare.miho-labs.dev | xargs kubectl delete   # optional
```
