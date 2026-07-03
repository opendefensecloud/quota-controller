# Plan: codify the s3.example.com provider (`root:providers:s3`) in Flux

**Status:** apply-later. The whole setup below was applied **manually** on sysdemo on
2026-07-02 and validated end-to-end (cyrill.berg can access buckets via the real OpenFGA
authz path; quota enforces 3/limit). This plan makes it survive a full kind/Flux redeploy.

**Why:** a governed service whose consumers are real accounts (e.g. `root:orgs:acme:demo`)
must live in a **proper platform-mesh provider** workspace under `root:providers`
(WorkspaceType `provider`). Only then does the workspace auto-bind `core.platform-mesh.io`,
which makes the **security-operator** watch it and generate the OpenFGA authorization model
for the bound resource — without which account members get `access denied / NoOpinion`.
See `memory/s3-platform-provider.md` for the root-cause detail.

Target repo: `cat/demo/flux-sysdemo-26.7` (GitOps). All manifests below already exist,
validated, in this session's scratchpad — copy them in.

---

## 1. Declare the workspace + its Flux kubeconfig (PlatformMesh CR)

File: `apps/components/platform-mesh-operator-resource/platform-mesh.yaml`

a) Under `spec.kcp.extraWorkspaces`, alongside the `root:providers:odd` entry, add:

```yaml
      - path: root:providers:s3
        type:
          name: provider
          path: root
```

b) In the kubeconfig-emitting list (where `root:providers:odd` emits
`kubeconfig-kcp-providers-odd-flux`), add an admin-scoped Flux kubeconfig for s3:

```yaml
      - path: root:providers:s3
        secret: kubeconfig-kcp-providers-s3-flux
```

Being WorkspaceType `provider`, `root:providers:s3` auto-binds `core.platform-mesh.io`
+ `dependencies.opendefense.cloud` (via the existing `extraDefaultAPIBindings` on
`root:provider`) — no extra binding needed for those.

---

## 2. New Flux component: `apps/components/s3-provider/`

Mirror `apps/components/arc-portal-content/`. Create these files (sources are in the
session scratchpad, already validated):

- `apiresourceschema-buckets.s3.example.com.yaml` — from `quota` repo
  `test/fixtures/apiresourceschema-buckets.s3.example.com.yaml`
- `apiexport-s3.example.com.yaml` — from `test/fixtures/apiexport-s3.example.com.yaml`,
  **plus** `metadata.labels: {ui.platform-mesh.io/content-for: s3.example.com}`. This label
  is REQUIRED for the marketplace tile: the marketplace VW joins each ProviderMetadata to an
  `apiexports?labelSelector=ui.platform-mesh.io/content-for=<providermetadata-name>`. Without
  it the provider has a ProviderMetadata + working ContentConfiguration but NO marketplace
  tile (this exact gap was the "s3 missing from marketplace" bug). Match arc, whose APIExport
  carries `ui.platform-mesh.io/content-for: arc.opendefense.cloud`.
- `providermetadata-s3.yaml` — scratchpad `s3-providermetadata.yaml` (name **must** equal
  the APIExport name `s3.example.com`; consider adding a light/dark icon like arc for a nicer tile)
- `contentconfiguration-s3-buckets.yaml` — scratchpad `s3-contentconfiguration.yaml`
  (labels `ui.platform-mesh.io/content-for: s3.example.com`,
  `ui.platform-mesh.io/entity: core_platform-mesh_io_account`)
- `apibinding-quota-provider.yaml` — binds `quota-provider` from `root:cloud-api-system`
  with the `validatingwebhookconfigurations` permissionClaim (scratchpad `s3-quota-harness.yaml`, first doc)
- `consumptionquota-buckets-limit3.yaml` — from `test/fixtures/consumptionquota-buckets-limit3.yaml`
- `provider-rbac.yaml` — the ClusterRole+ClusterRoleBinding granting the quota-controller
  (`User system:serviceaccount:quota-system:quota-controller`) get/list/watch on
  `apiexports`(+`/content`, resourceName `s3.example.com`), `apiresourceschemas`,
  `apiexportendpointslices` (scratchpad `s3-quota-harness.yaml`, docs 3–4)
- `apiexport-bind-rbac-s3.example.com.yaml` — ClusterRole+CRB granting the `bind` verb on
  the `s3.example.com` APIExport to `User system:anonymous` + `Group system:authenticated`
  (repo `test/fixtures/apiexport-bind-rbac-s3.example.com.yaml`). REQUIRED for UI-driven
  binding: kcp gates APIBinding creation with a `bind` deep-SAR in the provider workspace, so
  without this the marketplace "enable" button fails. Mirrors arc's
  `apps/components/arc-syncagent-kcp/apiexport-bind-rbac.yaml`.
- `kustomization.yaml` — `resources:` listing all of the above

**Ordering caveat (matches the manual run):** the `ConsumptionQuota` CRD is only served
after the `quota-provider` APIBinding is Bound. A single Flux Kustomization applies all
resources together; if the CQ 404s on first apply, Flux retries and it lands on the next
reconcile — acceptable. (Alternatively split the CQ into a second Kustomization
`dependsOn` the first.)

---

## 3. Flux Kustomization that applies §2 into the workspace

Add to a cluster overlay (e.g. new `clusters/<cluster>/s3.yaml`, mirroring the
`arc-portal-content` Kustomization block in `clusters/<cluster>/arc-syncagent.yaml`):

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: s3-provider
  namespace: flux-system
spec:
  interval: 1h
  retryInterval: 1m
  timeout: 3m
  dependsOn:
    - name: platform-mesh          # ensures the workspace + kubeconfig secret exist
    # - name: quota-controller     # add once the controller itself is Flux-managed
  sourceRef:
    kind: GitRepository
    name: sysdemo-cloudapi
  path: ./apps/components/s3-provider
  prune: false
  wait: false
  kubeConfig:
    secretRef:
      name: kubeconfig-kcp-providers-s3-flux
      key: kubeconfig
```

Add the same for every cluster overlay that has arc (`kind-local`, `sysdemo-cluster-01`).

---

## 4. Not codified here (call out)

- **quota-controller deploy** (helm into ns `quota-system`, the `quota-provider` APIExport +
  `QuotaUsage` CRD in `root:cloud-api-system`) is still manual — see
  `memory/deployment-context.md`. The `s3-provider` Kustomization depends on the
  `quota-provider` APIExport existing; codify the controller first (or `dependsOn` it).
- **Consumer binding** (`s3.example.com` APIBinding in `root:orgs:acme:demo`) stays
  manual/portal — `acme:demo` is portal-onboarded, not Flux-managed. Re-create it after a
  redeploy pointing at `root:providers:s3` (scratchpad `apibinding-s3-demo-providers.yaml`).
- **One ConsumptionQuota only** — the accounting manager builds a fixed `governed-usage`
  controller name; a second live CQ crashes reconcile.

---

## 5. Verify after Flux applies (same checks as the manual run)

```
# provider shape
kubectl --server .../clusters/root:providers:s3 get apiexport,providermetadata,contentconfiguration,consumptionquota
# authz (real request, impersonated)
kubectl --server .../clusters/root:orgs:acme:demo get buckets.s3.example.com -n default --as cyrill.berg@opendefense.cloud   # expect: authorized (empty ok)
# quota
# create 3 buckets ok as cyrill.berg; 4th -> "consumption quota exceeded: at most 3 buckets per workspace"
```
