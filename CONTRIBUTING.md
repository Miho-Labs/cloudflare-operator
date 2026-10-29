# Contributing

Thanks for helping. This document covers the development setup, the project conventions, and a
step-by-step guide for adding support for a new Cloudflare service.

## Development setup

Requirements: Go 1.26 (declared in `go.mod`; the Go toolchain downloads it on demand), `make`,
Docker for images, Helm for the chart. No `kubebuilder` binary is needed: code generation runs
through `go tool controller-gen`.

```bash
git clone https://github.com/miho-labs/cloudflare-operator
cd cloudflare-operator
make test            # unit tests, no cluster needed
make verify          # regenerate everything, fail on diff (CI runs this)
make run             # run the manager against your current kubeconfig
```

Useful targets: `make generate` (DeepCopy), `make manifests` (CRDs into the chart + RBAC), `make
helm-lint`, `make docker-build IMG=...`.

Tests do not need a cluster or a Cloudflare account: controllers run against the controller-runtime
fake client, and Cloudflare calls hit `internal/cloudflare/fake`, an in-memory API server.

## Repository layout

```
api/v1alpha1/            CRD Go types (+kubebuilder markers → CRDs, DeepCopy)
cmd/main.go              manager wiring: flags, indexes, controllers
internal/cloudflare/     SDK wrapper: client.go, errors.go, one file per service
internal/cloudflare/fake In-memory Cloudflare API for tests
internal/controller/     one reconciler per kind, common.go for shared helpers, *_test.go
internal/cloudflared/    cloudflared Deployment builder
charts/cloudflare-operator/  Helm chart; crds/ and templates/clusterrole.yaml are generated
config/rbac/role.yaml    controller-gen output consumed by hack/helmrbac
docs/                    user documentation, one page per resource
examples/                manifests used in the docs
```

## Conventions

- **Spec is the source of truth.** Reconcilers observe, diff, and only then write. Never write to
  Cloudflare without first comparing with an `Equal` method on the wrapper type.
- **Adopt, don't duplicate.** Before creating, look up by the natural key and adopt what exists.
- **Status holds external IDs** and `Ready`/`Synced` conditions; use `setReady`, `setNotReady`,
  `setSyncFailed`, `finish` from `common.go`.
- **Dependencies wait quietly** with `notReady(reason, message, requeueAfter)` instead of errors.
- **Deletion** goes through the shared finalizer and honours `spec.deletionPolicy`.
- **Secrets** the operator writes get `managedLabels()` and an owner reference.
- Keep SDK types inside `internal/cloudflare`; controllers only see the wrapper's plain structs.
- Every new Cloudflare call gets a handler in the fake server and a test.
- Run `make verify` before pushing; generated files are committed.

## Adding a new Cloudflare service

Example: a `WorkerRoute` kind that maps a route pattern to a Worker script.

### 1. Define the API type

Create `api/v1alpha1/workerroute_types.go` following the existing files:

```go
// WorkerRouteSpec defines a route that sends matching requests to a Worker.
type WorkerRouteSpec struct {
    AccountRef AccountRef `json:"accountRef"`
    Zone       ZoneRef    `json:"zone"`
    // Pattern such as "example.com/api/*".
    // +kubebuilder:validation:MinLength=1
    Pattern string `json:"pattern"`
    // Script name of the Worker.
    Script string `json:"script"`
    // +kubebuilder:default=Delete
    // +optional
    DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

type WorkerRouteStatus struct {
    ResourceStatus `json:",inline"`
    // +optional
    RouteID string `json:"routeID,omitempty"`
    // +optional
    ZoneID string `json:"zoneID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfroute
// +kubebuilder:printcolumn:name="Pattern",type=string,JSONPath=`.spec.pattern`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type WorkerRoute struct { ... }      // plus WorkerRouteList and init() registering both
```

Guidelines:
- Embed `ResourceStatus` in the status so the shared condition helpers work.
- Use `AccountRef`, `ZoneRef`, `DeletionPolicy`, `LocalObjectRef`, `AccessRuleRef` from
  `common_types.go` instead of inventing new reference shapes.
- Validate with markers (`+kubebuilder:validation:...`) and CEL (`+kubebuilder:validation:XValidation`)
  rather than webhooks.
- Add printer columns so `kubectl get` is useful.

Then `make generate manifests`. This writes DeepCopy methods, the CRD under
`charts/cloudflare-operator/crds/`, and regenerates RBAC.

### 2. Wrap the Cloudflare API

Add `internal/cloudflare/workers.go` with plain structs and the methods the controller needs:

```go
type WorkerRoute struct { ID, ZoneID, Pattern, Script string }

func (r WorkerRoute) Equal(o WorkerRoute) bool { return r.Pattern == o.Pattern && r.Script == o.Script }

func (c *Client) GetWorkerRoute(ctx, zoneID, id string) (*WorkerRoute, error)       // nil, nil when missing
func (c *Client) FindWorkerRoute(ctx, zoneID, pattern string) (*WorkerRoute, error) // natural key lookup
func (c *Client) CreateWorkerRoute(ctx, r WorkerRoute) (*WorkerRoute, error)
func (c *Client) UpdateWorkerRoute(ctx, r WorkerRoute) (*WorkerRoute, error)
func (c *Client) DeleteWorkerRoute(ctx, zoneID, id string) error                   // 404 is not an error
```

Prefer the typed SDK services (`c.api.Workers.Routes...`) with `cf.F(...)` fields. When the generated
parameter types are unusable (unions that hide required fields), use the generic request path like
`access.go` does: `c.api.Get/Post/Put/Delete(ctx, path, body, &envelope[T]{})`.

Map errors through `errors.go` (`IsNotFound`, `IsConflict`, `IsRateLimited`); wrap with context
(`fmt.Errorf("create worker route %s: %w", ...)`) so the condition message is readable.

### 3. Extend the fake API

Add the endpoints to `internal/cloudflare/fake/server.go`: a map for the objects, handlers registered
in `routes()`, and the real envelope shape (`writeJSON`, `writeList`, `writeError`). Mirror the real
API's status codes (404 for missing, 409 for duplicates). Add a round-trip test in
`internal/cloudflare/client_test.go`.

### 4. Write the reconciler

Copy `internal/controller/dnsrecord_controller.go`, the simplest complete example, into
`workerroute_controller.go` and adapt:

1. `Reconcile`: get → finalizer path if deleting → add finalizer → `finish(obj, &obj.Status, r.reconcile(ctx, obj), r.resync())` → `patchStatus`.
2. `reconcile`: `r.Clients.Resolve(ctx, obj.Spec.AccountRef)` → resolve zone → observe by ID, else
   adopt by natural key → create or update when `!current.Equal(desired)` → fill status → `setReady`.
3. `finalize`: honour `deletionPolicy`, delete on Cloudflare, remove the finalizer.
4. `SetupWithManager`: `For(&v1alpha1.WorkerRoute{})`, plus a `Watches` on `CloudflareAccount`
   using `requestsForAccount` so account changes re-queue the objects.
5. RBAC markers above `Reconcile` for the new resource, its `/status` and `/finalizers`.
6. Add the type to `AccountRefIndexer` in `dnsrecord_controller.go` and to the index lists in
   `cmd/main.go` and `internal/controller/suite_test.go`.
7. Register the reconciler in `cmd/main.go`.

Emit events (`EventCreated`, `EventAdopted`, `EventUpdated`, `EventDeleted`, `EventRetained`,
`EventFailed`) at the same points the other controllers do.

### 5. Test it

Add `workerroute_controller_test.go` using `newEnv(t, objs...)`, which gives you a fake Kubernetes
client pre-loaded with a verified account and a fake Cloudflare server. Cover at least:

- create → `Ready=True`, status IDs set, object exists in the fake server
- steady state does not call update (`e.cf.CallCount("PUT ")`)
- spec change updates in place; remote drift is corrected
- adoption of a pre-existing object
- delete with `Delete` and with `Retain`
- missing account / dependency → quiet requeue with the right reason

### 6. Document and wire the rest

- `make manifests` again (RBAC changed) and check `charts/cloudflare-operator/templates/clusterrole.yaml`.
- Add `docs/resources/workerroute.md` (spec table, status table, behaviour) and link it from
  `README.md` and the resource list.
- Add an example under `examples/` and reference it in the docs.
- If the service needs new token permissions, add a row to the table in
  `docs/getting-started.md`.
- Run `make verify && make test`.

### Checklist for the pull request

- [ ] Types with markers, printer columns and defaults
- [ ] Wrapper methods with `Equal`, `Get`/`Find`/`Create`/`Update`/`Delete`
- [ ] Fake API endpoints + client round-trip test
- [ ] Reconciler with adoption, drift correction, finalizer, events, RBAC markers
- [ ] Indexer + registration in `cmd/main.go` and `suite_test.go`
- [ ] Controller tests for the scenarios above
- [ ] Docs page, example, token permission row, README link
- [ ] `make verify` clean, generated files committed

## Releasing

Everything is published to the GitHub Container Registry by `.github/workflows/release.yaml`:

1. Bump `version` and `appVersion` in `charts/cloudflare-operator/Chart.yaml` and merge.
2. Tag and push: `git tag v0.2.0 && git push origin v0.2.0`.
3. The workflow builds the multi-arch image `ghcr.io/miho-labs/cloudflare-operator:0.2.0`
   (plus `0.2` and `latest`), packages the chart with that version and pushes it to
   `oci://ghcr.io/miho-labs/charts/cloudflare-operator`, and creates a GitHub release with the
   chart archive and a rendered `install.yaml`.

`ci.yaml` additionally pushes `:edge` and `:sha-<commit>` image tags from `main`.

**First release only:** GitHub creates organisation packages as private. After the first run, open
the org's *Packages* page, and for both `cloudflare-operator` and `charts/cloudflare-operator`
choose *Package settings → Change visibility → Public* and confirm the package is linked to this
repository (so the README and the `GITHUB_TOKEN` write access follow the repo). Public packages and
Actions minutes are free for public repositories.

Manual fallback: `make docker-build docker-push IMG=...` and
`helm registry login ghcr.io && make helm-push CHART_VERSION=0.2.0`.
