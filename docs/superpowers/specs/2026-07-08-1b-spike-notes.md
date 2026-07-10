# 1b spike notes — claim-discovery signal & maximalPermissionPolicy semantics

**Date:** 2026-07-08
**Task:** Iteration 1b, Task 1 (spike). Settles design spec §14.2 (discovery) and §14.5 (MPP verbs).
**Verdict:** Both hypotheses **hold**. Discovery decision for Task 7/8: **primary** (APIBindings via the governed service export's VW). Recorded as [ADR-005](../../../architecture/ADR-005-claim-discovery-via-service-export-vw.md).

## Environment (exact versions)

| Component | Version |
|---|---|
| kcp | **v0.32.3** (server reports `v1.36.0+kcp-v0.32.3`; image `ghcr.io/kcp-dev/kcp:v0.32.3`) |
| kcp-operator | v0.8.3 (Helm chart `kcp/kcp-operator` 0.7.7) |
| kind | v0.31.0 (node + workloads on Apple Silicon/arm64) |
| kubectl (client) | v1.36.1 |
| helm | v4.2.0 |
| etcd | v3.5.21 |

Harness: the repo's `make test-e2e` suite (kind + kcp-operator + front-proxy NodePort 31443 + helm-deployed 1a controller/webhook), run focused with
`E2E_SKIP_CLEANUP=1 make test-e2e testargs='-v -ginkgo.v -ginkgo.focus="spike|Scenario A"'`.
Final run: **`SUCCESS! -- 3 Passed | 0 Failed`** (Scenario A [1a enforcement] + the two spike specs). The throwaway spec lived in `test/e2e/spike_1b_test.go` and was deleted after the run, per the task plan.

## Question 1 — Is a consumer's APIBinding visible through the service export's VW at binding time?

**YES.** A fresh consumer workspace (`spike-consumer`, logical cluster `15rtbyq0bw5qrplg`) that bound
`root:s3-provider/s3.example.com` and **never created any governed object** (asserted: zero `buckets`
in the workspace) appears in the APIBinding list served by the service export's virtual workspace.

- VW URL resolved from the export's default `APIExportEndpointSlice` (auto-created by kcp, named
  after the APIExport): `https://<shard>/services/apiexport/ob45z1zsnj9oxwjy/s3.example.com`.
- List performed at `{vwURL}/clusters/*` as `kubectl get apibindings.apis.kcp.io -o json`.

Observed list (3 items = consumer1, consumer2 from the 1a suite + the fresh spike consumer):

```text
name=s3.example.com cluster=15rtbyq0bw5qrplg export=root:s3-provider/s3.example.com phase=Bound  <- fresh consumer
name=s3.example.com cluster=1e2d5ot5grlnw1x0 export=root:s3-provider/s3.example.com phase=Bound
name=s3.example.com cluster=xnrr0fb7oywwk4i7 export=root:s3-provider/s3.example.com phase=Bound
```

Output shape (the fields discovery needs):

- consumer logical cluster: `metadata.annotations["kcp.io/cluster"]` (same source `logicalcluster.From()` uses),
- export reference: `spec.reference.export.{path,name}`,
- `status.phase` (`Bound`).

Semantics confirmed against kcp source (`pkg/virtual/apiexport`): the VW **always** serves
`apibindings` (fallback in the apireconciler when the export does not claim them), backed by
read-only forwarding storage statically label-filtered to `internal.apis.kcp.io/export=<hash(exportCluster,exportName)>`
— i.e. exactly the bindings referencing *this* export, which kcp's APIBinding admission stamps at
create time (so visibility starts at binding creation, independent of any governed object).
Verbs are read-only for the export owner; a write attempt is rejected:

```text
Error from server (Forbidden): apibindings.apis.kcp.io "s3.example.com" is forbidden:
User "kcp-admin" cannot delete resource "apibindings" in API group "apis.kcp.io"
at the cluster scope: access denied
```

**No extra RBAC and no permissionClaim needed** beyond what the accounting reconciler already has
(`apiexports/content` get/list/watch on the governed export — the ADR-004 per-provider opt-in grant).

## Question 2 — Does the MPP verbs matrix (update/patch+read, read-only status, no create/delete) hold?

**YES.** Setup: throwaway APIExport `spike-mpp` in `root:quota-ctrl` re-exporting the
`ConsumptionQuota` schema (stand-in for `quotaclaims`), bound in the consumer workspace. Consumer
identity: user `spike-consumer-admin`, groups `[system:authenticated]`, made **cluster-admin inside
its own workspace** via a plain ClusterRoleBinding (the adversarial consumer-admin model).

### Accepted policy shape (the plan's `local.rules` shape is REJECTED)

The task plan's `spec.maximalPermissionPolicy.local.rules` (inline RBAC rules) does not exist in
kcp v0.32.3. Applying it fails with:

```text
Error from server (BadRequest): ... APIExport in version "v1alpha2" cannot be handled as a
APIExport: strict decoding error: unknown field "spec.maximalPermissionPolicy.local.rules"
```

The **actual accepted shape** is an empty marker plus ordinary RBAC objects in the export's
workspace whose subjects carry the `apis.kcp.io:binding:` prefix:

```yaml
# on the APIExport
spec:
  maximalPermissionPolicy:
    local: {}
---
# ClusterRole in the EXPORT workspace (root:quota-ctrl)
rules:
  - apiGroups: ["quota.opendefense.cloud"]
    resources: ["consumptionquotas"]          # stand-in for quotaclaims
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["quota.opendefense.cloud"]
    resources: ["consumptionquotas/status"]
    verbs: ["get", "list", "watch"]
---
# ClusterRoleBinding subject (covers every bound identity)
subjects:
  - kind: Group
    name: "apis.kcp.io:binding:system:authenticated"
```

Task 3 must produce this shape (APIExport `local: {}` + ClusterRole/ClusterRoleBinding YAML), not
inline rules. The verbs matrix itself is unchanged.

### Observed verb matrix (consumer-workspace admin, direct requests in its own workspace)

| Verb | Result | Evidence |
|---|---|---|
| get/list | ALLOWED | list used as the propagation gate before the denial assertions |
| update/patch (spec) | **ALLOWED** | `kubectl patch ... -p '{"spec":{"defaultLimit":9}}'` succeeded; persisted value re-read as `9` |
| create | **DENIED** | see below |
| delete | **DENIED** | see below |
| status patch | **DENIED** | see below |

Exact Forbidden messages (kcp v0.32.3):

```text
Error from server (Forbidden): error when creating "...": consumptionquotas.quota.opendefense.cloud is
forbidden: User "spike-consumer-admin" cannot create resource "consumptionquotas" in API group
"quota.opendefense.cloud" at the cluster scope: access denied

Error from server (Forbidden): consumptionquotas.quota.opendefense.cloud "spike-mpp-target" is
forbidden: User "spike-consumer-admin" cannot delete resource "consumptionquotas" in API group
"quota.opendefense.cloud" at the cluster scope: access denied

Error from server (Forbidden): consumptionquotas.quota.opendefense.cloud "spike-mpp-target" is
forbidden: User "spike-consumer-admin" cannot patch resource "consumptionquotas/status" in API group
"quota.opendefense.cloud" at the cluster scope: access denied
```

Note: being cluster-admin in the consumer workspace does **not** bypass the policy — kcp evaluates
the MPP *above* the local/bootstrap RBAC chain, so workspace-local grants cannot widen it (only
`system:masters` via the always-allow authorizer bypasses it).

### Owner writes through the export VW are NOT restricted by the MPP

The export owner pre-created the instance through the VW (`{spikeVW}/clusters/<consumerCluster>`)
**without any owner-specific MPP grant**:

```text
owner VW create WITHOUT owner MPP grant: err=<nil>  ->  consumptionquota.../spike-mpp-target created
```

and wrote `status` the same way (also succeeded). A subsequent consumer status-forgery attempt was
denied and the owner-written status survived. This confirms design spec §10's assumption ("the
controller writes via the export VW, not subject to the policy") — mechanism: the VW forwards
requests with a `system:kcp:admin`-group warrant, which satisfies authorization independently of
the prefixed-identity MPP check. **Consequence for Task 7/8:** the controller's claim pre-creation
and status writes need no MPP carve-out; no `apis.kcp.io:binding:<controller>` RBAC is required.

## DECISION (consumed by Tasks 7–8)

**Primary hypothesis adopted — no fallback needed.** Claim discovery lists/watches consumers'
`APIBinding`s through the **governed service APIExport's virtual workspace** at `{vwURL}/clusters/*`:

- Signal available at *binding* time, before any governed object exists (R11 visibility holds).
- Consumer cluster from the `kcp.io/cluster` annotation; already-verified read path
  (`apiexports/content` on the governed export) — no new grants, no permissionClaim on any quota
  export, nothing a consumer can decline.
- Unbind removes the APIBinding from the VW list → discovery diff drives claim GC (spec §14.2).
- Task 8 implements this as a periodic list-based sweep per accounting sub-manager (`resync`
  interval bounds the claim-creation delay after bind).

## Related posture notes

- **Identity rotation (spec §14.3):** no code in 1b beyond stale-ref fail-closed behavior — claims/
  grants keyed on the old identity tuple simply stop matching after a rotation. The `IdentityRotated`
  condition on `ConsumptionQuota` is deferred to a CQ-reconciler follow-up if rotation is ever
  observed in practice.
- **Harness drift found while running the spike** (pre-existing 1a e2e issues, fixed in the working
  tree alongside this spike; not part of the docs commit): kubectl ≥1.36 requires `apply -k` for
  `config/kcp` (kustomization.yaml is otherwise applied as a resource); `test/fixtures/integration-values.yaml`
  still used the pre-refactor top-level `image:` key (chart consumes `controller.image`/`webhook.image`);
  the suite never applied the ADR-004 per-provider RBAC (`provider-rbac-s3.example.com.yaml`, new
  fixture) nor the `QuotaUsage` CRD in `root:quota-ctrl` — without these, 1a enforcement never
  started (`Scenario A` failed with the controller unable to read the governed APIExport, then with
  `no matches for kind "QuotaUsage"`). With the fixes, the focused 1a spec passes.
