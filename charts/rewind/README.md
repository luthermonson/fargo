# rewind Helm chart

Deploys [rewind](https://github.com/luthermonson/rewind): a controller that
makes rollouts transactional on a k3s-on-beads cluster by bracketing each one
in a scoped [kine](https://github.com/luthermonson/kine) transaction.

## Install

```sh
# argo mode, watching Applications in-cluster, talking to a kine over TCP
helm install rewind charts/rewind \
  --namespace rewind-system --create-namespace \
  --set mode=argo \
  --set kine.endpoint=http://192.168.5.2:2381 \
  --set kine.tokenSecret.name=rewind-kine-token
```

The kine token and any kubeconfigs are read from **mounted Secrets** — the
chart never inlines a secret value. Create the token Secret yourself:

```sh
kubectl -n rewind-system create secret generic rewind-kine-token \
  --from-file=token=/path/to/kine.token
```

To watch a **remote** management cluster (Argo lives elsewhere), put its
kubeconfig in a Secret and set `management.kubeconfigSecret.name`. For `argo`
and `flux`, the target (managed) cluster is used for kind→resource discovery;
set `target.kubeconfigSecret.name` if it differs from the cluster rewind runs
in.

## RBAC granted per mode

| Mode | ClusterRole rules (beyond events + leader-election leases) |
|------|-----------------------------------------------------------|
| `argo` | `argoproj.io/applications`: get, list, watch, patch |
| `flux` | `kustomize.toolkit.fluxcd.io/kustomizations` and `helm.toolkit.fluxcd.io/helmreleases`: get, list, watch, patch |
| `crd`  | `rewind.gascity.dev/transactions` (+ `/status`): get, list, watch, create, update, patch, delete |

All modes also get `""/events` create+patch and `coordination.k8s.io/leases`
for leader election. `crd` mode installs the `Transaction` CRD when
`installCRD=true`.

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `mode` | `""` | **Required.** `argo`, `flux` or `crd`. |
| `replicaCount` | `1` | Replicas (use `leaderElect` with >1). |
| `image.repository` | `ghcr.io/luthermonson/rewind` | Image repo. |
| `image.tag` | `""` | Defaults to the chart `appVersion`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `kine.endpoint` | `http://192.168.5.2:2381` | kine control API; `http://host:port` or `unix:///path/kine.sock`. |
| `kine.tokenSecret.name` | `""` | Existing Secret with the bearer token (empty = none). |
| `kine.tokenSecret.key` | `token` | Key within that Secret. |
| `management.kubeconfigSecret.name` | `""` | Secret with a kubeconfig; empty = in-cluster. |
| `management.kubeconfigSecret.key` | `kubeconfig` | Key within that Secret. |
| `management.context` | `""` | Context within the management kubeconfig. |
| `target.kubeconfigSecret.name` | `""` | Managed-cluster kubeconfig (argo/flux); empty = in-cluster. |
| `target.kubeconfigSecret.key` | `kubeconfig` | Key within that Secret. |
| `healthDeadline` | `5m` | Wait for health before rolling back. |
| `namespaceScope` | `auto` | `auto` adds the destination namespace to scope; `none` does not. |
| `namespace` | `""` | Namespace to watch (empty = all). |
| `dryRun` | `false` | Log begin/commit/rollback without sending. |
| `metricsBindAddress` | `:8080` | `/metrics`, `/healthz`, `/readyz`. |
| `leaderElect` | `true` | Leader election. |
| `leaderElectId` | `""` | Lease name; defaults to `rewind-<mode>`. |
| `installCRD` | `true` | Install the Transaction CRD (crd mode only). |
| `serviceAccount.create` | `true` | Create a ServiceAccount. |
| `rbac.create` | `true` | Create the ClusterRole/Binding. |
| `resources` | 50m/64Mi → 200m/128Mi | Container resources. |
| `podSecurityContext` / `securityContext` | restricted | PodSecurity-friendly defaults. |

See [`values.yaml`](values.yaml) for the full set.
