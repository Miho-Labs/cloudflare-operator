# cloudflare-operator

A Kubernetes operator that manages Cloudflare resources from custom resources, using a single API
token and account ID per `CloudflareAccount`.

| Kind | What it manages |
|------|-----------------|
| `CloudflareAccount` | Credentials: account ID + API token Secret. Every other resource points at one. |
| `Tunnel` | A remotely-managed Cloudflare Tunnel and (by default) a `cloudflared` Deployment that runs it in-cluster. |
| `TunnelIngress` | A public hostname ("app") served through a Tunnel, including its proxied CNAME record. |
| `DNSRecord` | Any DNS record in a zone. Works on its own or alongside tunnels. |
| `R2Bucket` | An R2 bucket plus an optional bucket-scoped API token, delivered as S3 credentials in a Secret. |
| `AccessApplication` | A Zero Trust Access self-hosted application with inline policies. |
| `AccessGroup` | A reusable Access group referenced by policies. |
| `AccessServiceToken` | An Access service token for machine clients, stored in a Secret and refreshed before expiry. |

Every resource reports `Ready`/`Synced` conditions, adopts pre-existing Cloudflare objects with the
same natural key instead of duplicating them, corrects drift on a periodic resync, and honours
`deletionPolicy: Delete|Retain`.

## Quick start

```bash
helm upgrade --install cloudflare-operator ./charts/cloudflare-operator \
  --namespace cloudflare-operator --create-namespace
```

Create the token Secret and the account (see [Getting started](docs/getting-started.md) for how to
create a token with the right permissions and where to find your Account ID):

```bash
kubectl -n cloudflare-operator create secret generic cloudflare-api-token \
  --from-literal=apiToken='<your token>'
```

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
```

```bash
kubectl get cloudflareaccounts
# NAME   ACCOUNT ID                         ACCOUNT        READY   AGE
# main   0123456789abcdef0123456789abcdef   Acme Inc       True    10s
```

Expose a Service to the internet through a tunnel:

```yaml
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: Tunnel
metadata:
  name: edge
  namespace: default
spec:
  accountRef:
    name: main
---
apiVersion: cloudflare.miho-labs.dev/v1alpha1
kind: TunnelIngress
metadata:
  name: app
  namespace: default
spec:
  tunnelRef:
    name: edge
  hostname: app.example.com
  service:
    name: app
    port: 8080
```

The operator creates the tunnel, runs two `cloudflared` replicas with its token, pushes the ingress
rule to the tunnel configuration and creates the proxied CNAME `app.example.com`. Add an
`AccessApplication` for `app.example.com` to put it behind Zero Trust. More in [examples/](examples/).

## Documentation

- [Getting started](docs/getting-started.md): API token permissions, finding the Account ID, install, first resources.
- [Architecture](docs/architecture.md): how reconciliation, adoption, drift correction and deletion work.
- Resource reference: [CloudflareAccount](docs/resources/cloudflareaccount.md),
  [Tunnel](docs/resources/tunnel.md), [TunnelIngress](docs/resources/tunnelingress.md),
  [DNSRecord](docs/resources/dnsrecord.md), [R2Bucket](docs/resources/r2bucket.md),
  [AccessApplication](docs/resources/accessapplication.md), [AccessGroup](docs/resources/accessgroup.md),
  [AccessServiceToken](docs/resources/accessservicetoken.md).
- [Troubleshooting](docs/troubleshooting.md)
- [Contributing](CONTRIBUTING.md): development setup and a step-by-step guide for adding a new Cloudflare service.

## Requirements

- Kubernetes 1.28 or newer.
- A Cloudflare account with the zones you want to manage. Zero Trust features need a Zero Trust
  organisation (the free plan is enough for Access and tunnels).
- An API token with the permissions listed in [Getting started](docs/getting-started.md#2-create-an-api-token).

## Development

```bash
make test       # unit tests against an in-memory fake Cloudflare API
make verify     # regenerate CRDs/RBAC/deepcopy and fail on diff
make build      # bin/manager
make run        # run against the current kubeconfig
```

Requires Go 1.26 (the Go toolchain downloads it automatically when `go.mod` asks for it).

## License

Apache-2.0. See [LICENSE](LICENSE).
