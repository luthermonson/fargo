# rewind

**Transactional deployments for Kubernetes, on a datastore that can actually
undo them.**

rewind is a controller that brackets every rollout in a database transaction.
When a deployment starts, it opens a *scoped savepoint* in the cluster's
datastore. When the rollout becomes healthy, it commits. When the rollout
fails — a bad image, a failed health check, a deadline — it **rolls the
cluster back** to exactly the objects that existed before the deployment began,
with their original UIDs, as new revisions on the live timeline. No process
restarts; the deployment system's own objects are never touched.

It runs on **k3s-on-beads**: a real k3s cluster whose datastore is
[`kine`](https://github.com/luthermonson/kine) (branch `beads-backend`), which
stores every Kubernetes object as a *bead* in a Dolt-versioned work-graph. The
companion gate is [`kourt`](https://github.com/luthermonson/kourt).

> Status: prototype. rewind builds against the **kine control API v0** contract
> for scoped transactions (`/txn/*`). Those endpoints are still being built in
> kine; until they are live, run rewind with `--dry-run` or against the fake
> kine in the tests. The three mode mappers, the reconcile engine, the chart
> and the demo are complete and tested.

## Why this exists

On a normal cluster, "rolling back a deployment" means one of:

- **`git revert` + re-sync.** This only reverts what is in git. The controller
  already acted: it created ReplicaSets, scaled things, wrote status, fired
  webhooks, left events. Reverting the manifest asks the controller to drive
  *forward* to a previous desired state; it does not restore what was there.
  Objects come back with new UIDs; anything the failed rollout created and the
  new desired state does not mention may linger.
- **`kubectl rollout undo`.** Workload-local, Deployment-only, and still a
  forward reconcile, not a restore.

The substrate makes something better possible. Every write to the datastore is
already a **commit** in a Dolt-backed store, every object is a revision-stamped
bead, and `ownerReferences` are parent–child edges in the graph. So the store
*can* be asked: "what changed, within this set of objects, since that commit?"
and the inverse can be emitted as fresh writes. That is a scoped transaction.

## What a scoped transaction is (and is not)

`begin` records the datastore commit at that instant (the **savepoint**) and a
**scope** — the set of keys the transaction is allowed to revert. Writes keep
landing on the live branch the whole time; nothing is frozen and nothing is
branched.

`rollback` diffs savepoint → now, **filtered to the scope** (and, with
`expandOwners`, the ownership cone of the scope), and emits the inverse as new
revisions:

- created since the savepoint → **delete**
- modified → restore the value **as of** the savepoint (same UID)
- deleted → **recreate** it

Out-of-scope keys are never touched, so Argo/Flux's own objects, other apps,
nodes, and system state keep moving. `commit` simply forgets the savepoint —
the writes were on the live branch all along, so there is nothing to merge.

**What it cannot undo:** anything a controller did *outside* the datastore — a
cloud VM, a DNS record, a webhook call, a sent email. rewind reverts cluster
state, not the world. (This is why the demo uses podinfo and a non-existent
image: the failure is entirely in-cluster.) Node-local state *follows* the
revert — pods whose objects vanish are stopped by the kubelet — but it is not
restored byte-for-byte.

## The three modes

rewind watches one deployment system, selected by `--mode`, and maps its
objects to three rollout events — `started(id, scope)`, `succeeded(id)`,
`failed(id, reason)` — which a shared engine drives through begin → health-wait
→ commit / rollback.

### `--mode argo`

Watches `applications.argoproj.io/v1alpha1` on the management cluster.

| Event | Trigger |
|-------|---------|
| begin | `status.operationState.phase == Running` with a sync operation set |
| commit | `phase == Succeeded` **and** `status.health.status == Healthy` within `--health-deadline`, where the health comes from a reconcile that ran *after* the operation finished (`status.reconciledAt >= operationState.finishedAt`, synced revision = operation revision); the pre-sync health is never trusted |
| rollback | `phase == Failed`/`Error`, `health == Degraded` (same freshness rule), or the deadline |
| in progress | `phase == Succeeded` with any other (or stale) health: no action, but after a controller restart the open transaction is re-adopted from the ledger and its deadline re-armed |

Scope comes from `status.resources[]` (each `group`, `kind`→resource via the
target cluster's discovery, `namespace`, `name`), plus `spec.destination.namespace`
as an in-scope namespace when `--namespace-scope auto`. The transaction id is
`argo/<app-namespace>/<app-name>/<operationState.operation.sync.revision>`.
rewind annotates the Application with `rewind.gascity.dev/txn: <id>` and
`rewind.gascity.dev/outcome: committed|rolledBack|open` — the only writes it
makes to the management cluster.

Argo can begin *before* apply with a **PreSync hook**: ship
[`examples/argo/presync-hook.yaml`](examples/argo/presync-hook.yaml) in the
app. It blocks the sync until `GET /txn` shows an open transaction for the
Application, so apply can never start before the savepoint. The hook needs the
kine endpoint and token too.

### `--mode flux`

Watches `kustomizations.kustomize.toolkit.fluxcd.io/v1` and
`helmreleases.helm.toolkit.fluxcd.io/v2`.

| Event | Trigger |
|-------|---------|
| begin | `status.lastAttemptedRevision` set and `Reconciling=True` (or `Ready=Unknown`, reason Progressing) |
| commit | `Ready=True` and `lastAppliedRevision == lastAttemptedRevision` |
| rollback | `Ready=False` (BuildFailed, HealthCheckFailed, InstallFailed, UpgradeFailed, ReconciliationFailed, …) or the deadline |

Scope comes from `status.inventory.entries[].id`
(`<namespace>_<name>_<group>_<kind>`) plus the target/release namespace. The id
is `flux/<namespace>/<name>/<lastAttemptedRevision>`.

**Caveat:** Flux has no pre-apply hook, so there is a small window where apply
can precede `begin`. rewind narrows it by reacting on the *reconciling*
condition the instant it appears (before the apply completes), but it cannot
close it entirely. For an airtight savepoint, use `crd` mode.

### `--mode crd`

An explicit `transactions.rewind.gascity.dev/v1alpha1` object, for kubectl and
CI users — the airtight mode, because nothing races the controller.

- create it (with `spec.scope`) → **begin** (`status.phase: Open`)
- set `spec.outcome: commit` or `rollback` → **finish**
- `kubectl delete` it while `Open` → **rollback**

`status` carries `phase` (Pending, Open, Committed, RolledBack, Failed),
`txnId`, `baseRev` and `message`. The CRD is in
[`config/crd/`](config/crd/) and in the chart. See
[`examples/crd/transaction.yaml`](examples/crd/transaction.yaml).

## Configuration

Flags, `REWIND_*` environment variables and Helm values all map to the same
settings — endpoints are reconfigurable without a code change, and the
Deployment reads kubeconfigs and the kine token from mounted Secrets, so
swapping the management cluster or the kine address is a values change plus a
rollout.

| Flag | Env | Purpose |
|------|-----|---------|
| `--mode` | `REWIND_MODE` | `argo`, `flux` or `crd` (required) |
| `--management-kubeconfig` | `REWIND_MANAGEMENT_KUBECONFIG` | where Applications/Kustomizations/HelmReleases/Transactions are watched (default: in-cluster) |
| `--management-context` | `REWIND_MANAGEMENT_CONTEXT` | context within that kubeconfig |
| `--target-kubeconfig` | `REWIND_TARGET_KUBECONFIG` | managed cluster, for discovery/health (default: in-cluster) |
| `--kine-endpoint` | `REWIND_KINE_ENDPOINT` | `http://host:port` or `unix:///path/kine.sock` |
| `--kine-token-file` | `REWIND_KINE_TOKEN_FILE` | file with the bearer token (mounted from a Secret) |
| `--health-deadline` | `REWIND_HEALTH_DEADLINE` | wait this long for health before rolling back (default `5m`) |
| `--namespace-scope` | `REWIND_NAMESPACE_SCOPE` | `auto` (add destination namespace to scope) or `none` |
| `--namespace` | `REWIND_NAMESPACE` | namespace to watch (empty = all) |
| `--dry-run` | `REWIND_DRY_RUN` | log begin/commit/rollback without sending |
| `--metrics-bind-address` | `REWIND_METRICS_BIND_ADDRESS` | `/metrics`, `/healthz`, `/readyz` (default `:8080`) |
| `--leader-elect` | `REWIND_LEADER_ELECT` | run leader election |

The **management cluster** is where the deployment system's objects live (Argo
may run somewhere other than the cluster it deploys to). The **target cluster**
is the managed one; rewind uses its discovery to map a `Kind` to a resource for
scope keys. Both default to in-cluster.

The transaction client speaks the kine control API v0 over TCP or a unix
socket, with the bearer token from `--kine-token-file`. The token is never
defaulted to a real value — mount it from a Secret.

## Quickstart

```sh
make build        # static ./bin/rewind
make test         # go test -race ./...
make helm-lint    # lint + template all three modes
```

### argo

```sh
helm install rewind charts/rewind -n rewind-system --create-namespace \
  --set mode=argo \
  --set kine.endpoint=http://192.168.5.2:2381 \
  --set kine.tokenSecret.name=rewind-kine-token
```

### flux

```sh
helm install rewind charts/rewind -n rewind-system --create-namespace \
  --set mode=flux --set kine.endpoint=http://192.168.5.2:2381
```

### crd

```sh
helm install rewind charts/rewind -n rewind-system --create-namespace \
  --set mode=crd --set kine.endpoint=http://192.168.5.2:2381
kubectl apply -f examples/crd/transaction.yaml
```

See [`charts/rewind/README.md`](charts/rewind/README.md) for every value and
the RBAC each mode is granted.

## Demo

[`luthermonson/rewind-demo`](https://github.com/luthermonson/rewind-demo) is a
tiny GitOps repo both Argo and Flux can point at: podinfo on branch `main`
(good) and branch `broken` (a non-existent image tag). The flow:

1. Sync `main`: rewind begins a transaction, podinfo goes healthy, it commits —
   nothing visible changed.
2. Sync `broken`: rewind begins, the pod lands in `ImagePullBackOff`, the app
   goes Degraded, rewind rolls back. `kubectl get all` shows the **previous**
   ReplicaSet and Pods with their **original UIDs**, and `bd history` on the
   Deployment's bead still shows the failed attempt in the timeline.

## Architecture

```
            management cluster                    target (managed) cluster
         ┌───────────────────────┐             ┌──────────────────────────┐
 argo →  │ Applications          │             │  k3s apiserver           │
 flux →  │ Kustomizations/HR     │  discovery  │     │                    │
 crd  →  │ Transactions (CRD)    │◄────────────┤  kine-beads  ── dolt     │
         └──────────┬────────────┘             └─────────┬────────────────┘
                    │ informers                          │ /txn/* (control API v0)
            ┌───────▼─────────────────────────────────────▼──────┐
            │ rewind                                              │
            │  pkg/modes/{argo,flux,crd}  obj → Signal            │
            │  pkg/reconcile              begin/commit/rollback   │
            │  pkg/txn                    kine control API client │
            └────────────────────────────────────────────────────┘
```

```
cmd/rewind/        CLI (urfave/cli v2) → config → controller
pkg/config/        flags/env/values → one Config
pkg/txn/           kine control API v0 client + scope builder
pkg/reconcile/     the engine: begin once, commit on healthy, roll back on
                   failure/deadline; idempotent against the ledger
pkg/modes/         argo, flux, crd mappers (pure, fixture-tested)
pkg/controller/    k8s clients, dynamic informers, discovery, annotator,
                   leader election, metrics
pkg/txntest/       a fake kine control API for tests
config/crd/        the Transaction CRD
charts/rewind/     Helm chart (per-mode RBAC, Secret-mounted token/kubeconfig)
examples/          Argo PreSync hook, a Transaction CR
```

Idempotency is anchored in the kine **ledger**: the engine does `GET /txn/{id}`
before `begin`, so a controller restart never double-begins and never rolls
back a committed transaction; a transaction opened by a previous instance is
adopted.

## Related

- [`luthermonson/kine`](https://github.com/luthermonson/kine) (branch
  `beads-backend`) — the k3s datastore on beads; hosts the control API v0.
- [`luthermonson/kourt`](https://github.com/luthermonson/kourt) — the
  causality-first test gate that decides whether a rollout (or a fork) passed.
- [`luthermonson/rewind-demo`](https://github.com/luthermonson/rewind-demo) —
  the demo GitOps repo.

## License

MIT. See [LICENSE](LICENSE).
