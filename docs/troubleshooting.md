# Troubleshooting

Start with the conditions and events of the resource:

```bash
kubectl describe <kind> <name>
kubectl get events --field-selector involvedObject.name=<name>
kubectl -n cloudflare-operator logs deploy/cloudflare-operator
```

| Symptom | Cause | Fix |
|---------|-------|-----|
| `CloudflareAccount` `Ready=False`, message mentions `Authentication error` or `403` | Token invalid, expired, or missing *Account Settings: Read* / wrong account ID | Re-check the token at `/user/tokens/verify`, confirm `spec.accountID` matches the dashboard. |
| Resource stuck in `AccountNotReady` | The referenced account is not verified | Fix the account first; dependents retry every 30 s. |
| `AccountNotFound` | `spec.accountRef.name` does not match a `CloudflareAccount` | `kubectl get cloudflareaccounts`. |
| `DNSRecord` error `zone "x" not found in account` | Zone not in this account or token lacks *Zone: Read* | Check the zone, extend the token's zone resources. |
| `TunnelIngress` stays `DependencyNotReady: waiting for Tunnel to push hostname` | Tunnel not ready, or the TunnelIngress is in another namespace | Tunnels and ingresses must share a namespace; check the Tunnel's conditions. |
| `Tunnel` error `exists but is locally managed` | A tunnel with that name was created with a local `config.yml` | Rename the Tunnel (`spec.name`) or delete/migrate the old tunnel. |
| cloudflared pods crash-loop with `Unauthorized` | Token Secret stale after the tunnel was recreated | The operator rewrites the Secret on the next reconcile; delete the pods. |
| `connectorStatus: down` while pods run | Egress to `*.argotunnel.com:7844` (QUIC/UDP) blocked | Allow UDP 7844 or set `spec.cloudflared.protocol: http2`. |
| `R2Bucket` error `permission group ... not found` | Operator token lacks *Account API Tokens: Edit* | Add the permission; needed to create bucket-scoped tokens. |
| `R2Bucket` stuck `Deleting`, message `bucket is not empty` | Cloudflare refuses to delete non-empty buckets | Empty the bucket or set `deletionPolicy: Retain`. |
| `AccessApplication` error `domain_conflict` | Another app already protects the domain and was not adopted (different path form) | Use the exact `domain` string of the existing application so it is adopted. |
| HTTP 429 in conditions | Cloudflare rate limit (1200 req / 5 min per token) | Reconciles back off automatically; raise `operator.resyncPeriod` for large fleets. |
| Changes made in the dashboard keep reverting | Expected: the spec is the source of truth | Edit the Kubernetes object, or delete it with `deletionPolicy: Retain`. |

## Running with verbose logs

```bash
helm upgrade cloudflare-operator ./charts/cloudflare-operator -n cloudflare-operator \
  --reuse-values --set operator.logLevel=debug
```

## Checking what the operator sent

The fake Cloudflare API used in tests records every call. For a real account, the Cloudflare
dashboard's **Audit Log** (Manage Account → Audit Log) lists every API change with the token name.
