# kcp Consumption Quota — Design

**Status:** Draft (brainstorming output, pending review)
**Date:** 2026-06-29
**Related work:** [`dependency-controller`](https://github.com/opendefensecloud/dependency-controller) — this design deliberately reuses its topology, RBAC model, and webhook-installation machinery.

## 1. Problem

In a [kcp](https://kcp.io) environment, service providers expose APIs via `APIExport`s.
Consumer workspaces bind to one or more provider `APIExport`s and create instances of
the exported types in their own workspaces.

The provider needs to **cap how many instances each consumer workspace may create**, and
the cap must be **defined by the provider**, not the consumer.

- **Iteration 1 (this spec):** cap by **count** — "a consumer workspace may consume at
  most N instances of resource type X."
- **Iteration 2 (designed for, not built):** cap by an **aggregate property** — "a consumer
  may only consume buckets with aggregated size < 1 TiB."

## 2. Requirements (decided during brainstorming)

| # | Requirement | Decision |
|---|-------------|----------|
| R1 | Enforcement strictness | **Strict — the limit may never be exceeded, even momentarily.** |
| R2 | What is capped | **Admitted API objects.** The `CREATE` call itself must be rejected at the API; nothing is stored once at the limit. (Not merely "actually-provisioned" resources.) |
| R3 | Threat model | **Adversarial consumers, no RBAC control.** Consumers are full admins of their own workspaces and may actively try to bypass the quota. The enforcement authority must therefore live **outside** the consumer workspace. |
| R4 | Limit granularity | **Single default limit per resource type**, applied uniformly to every consumer. *(Per-consumer overrides: future improvement, see §10.)* |
| R5 | Counting scope | **Per consumer workspace** (logical cluster), aggregated across all its namespaces. *(Per-namespace: future improvement, see §10.)* |
| R6 | Availability posture | **Fail-closed.** While the webhook is unavailable, governed `CREATE`s are rejected. This is the unavoidable price of R1. |
| R7 | Quota authorship | Provider-defined, created alongside the provider's `APIExport`. |

## 3. Decision summary & alternatives considered

R1–R3 compose into a hard conclusion that eliminates the simpler options:

- **Injected `ResourceQuota` in the consumer workspace** — kcp-native and genuinely atomic
  (runs inside the apiserver admission/etcd path). **Rejected** by R3: the object lives in
  the consumer's own workspace, and an adversarial workspace-admin would edit or delete it.
  Continuous reconciliation cannot close the tamper window. *(Would be the simplest option
  if the trust model ever changes to "trusted" or "adversarial but platform controls
  workspace RBAC" — worth revisiting then.)*
- **Stateless live-count webhook** (count objects on each admission, like dep-ctrl's
  deletion check) — **Rejected** by R1: a live `LIST` races against not-yet-persisted
  creates (TOCTOU). Two concurrent `CREATE`s both observe "2 of 3" and both succeed.
- **Actuation-gating** (admit the object, but the provider's controller refuses to
  provision beyond N) — **Rejected** by R2: this caps provisioned resources, not admitted
  objects; the `CREATE` is not rejected at the API.

**Chosen approach: an external, stateful admission webhook installed in the provider
workspace**, holding authoritative accounting in a **persisted `QuotaUsage` object** and
reserving slots via **optimistic-concurrency (compare-and-swap)**.

### Honest caveat on atomicity

No external webhook is *truly* atomic with the apiserver write the way native
`ResourceQuota` is. Strictness is achieved by making the webhook the **sole serialization
point**: every governed `CREATE` across every shard funnels to one webhook endpoint, the
authoritative count lives in a single CAS-protected object, and the webhook fails closed.
The reservation mechanism (§6) is what closes the TOCTOU race that a live count cannot.

## 4. Topology

Identical to the dependency-controller. The only structural addition is the internal
`QuotaUsage` accounting type and a watch on the provider's `APIExport` virtual workspace.

```
quota-ctrl WS ──► APIExport: ConsumptionQuota
                    + permissionClaim: validatingwebhookconfigurations
                  CRD (internal): QuotaUsage   ← authoritative accounting, outside consumer reach

provider WS   ──► APIBinding: quota-ctrl (permissionClaim accepted)
                  APIExport: <their service>            (e.g. s3.example.com → buckets)
                  ConsumptionQuota: "buckets ≤ 3 per consumer"
                  ValidatingWebhookConfiguration        ← installed by the controller (CREATE on buckets)

consumer WS   ──► APIBinding: <provider service>; creates Buckets
                  (never interacts with the quota system)
```

**The reused core trick:** a `ValidatingWebhookConfiguration` installed in the *provider*
workspace intercepts operations on the exported type across **all** consumer workspaces
bound to that provider, and consumers cannot tamper with it because it lives in the
provider's workspace. This is exactly the dependency-controller's deletion-protection
mechanism, applied to `CREATE`.

## 5. Components

Three logical components. The controller and accounting reconciler may share one binary;
the webhook is a separate binary (mirrors dep-ctrl's `cmd/controller` + `cmd/webhook`).

### 5.1 Controller (`cmd/controller`)
Reuses dep-ctrl's `WebhookInstaller`, retargeted:
- Watches `ConsumptionQuota` objects via the quota-ctrl `APIExport` virtual workspace.
- Installs/updates a `ValidatingWebhookConfiguration` named `consumption-quota` in the
  provider workspace, scoped to **`CREATE`** on the governed GVR, authorized by the
  `validatingwebhookconfigurations` permissionClaim.
- Resolves `apiExportName`/path → logical cluster name exactly as dep-ctrl does (same known
  limitation re: workspace nesting applies; see §11).

### 5.2 Accounting reconciler (new)
- Watches the **governed objects across all consumers via the provider's `APIExport`
  virtual workspace** — a single cross-consumer watch stream (the idiomatic kcp mechanism).
- Maintains `status.confirmed` in each `QuotaUsage` = the true number of live objects for
  that (consumer, type); folds in / sweeps reservations (§6). It owns `confirmed` only —
  the limit lives in the policy, read by the webhook (§5.3).
- Should be single-writer (leader-elected) to avoid two reconcilers fighting; its writes
  are idempotent (`confirmed = trueCount`) so transient overlap is safe.

### 5.3 Webhook (`cmd/webhook`)
- Watches all `ConsumptionQuota` policies via the quota-ctrl VW into an **in-memory
  registry** indexed by governed GVR → limit (directly analogous to dep-ctrl's
  `RuleRegistry`). This is the authoritative source of the limit on the hot path — no
  per-request policy read, and no bootstrap race when a `QuotaUsage` does not exist yet.
- On each `CREATE`: look up the limit from the registry, then `GET`/create-and-CAS the one
  `QuotaUsage` object in the quota-ctrl workspace (§6). It does **not** list consumer
  resources.
- Consequence vs dep-ctrl: the webhook needs **no broad cross-workspace read RBAC**. Only
  the reconciler needs cross-consumer read (via the provider VW). Simpler and faster on the
  admission hot path.
- Identifies the consumer from the `kcp.io/cluster` annotation on the admission request
  (same as dep-ctrl).

## 6. Enforcement protocol (CAS reservations)

This is the heart of the design — what makes strict enforcement possible from outside the
apiserver.

### 6.1 Accounting object

`QuotaUsage`, keyed by `(consumerCluster, gvr)`, stored in the quota-ctrl workspace. It
holds **accounting only** — the limit lives in the `ConsumptionQuota` policy (read by the
webhook from its registry, §5.3):

```yaml
status:
  confirmed: 2              # owned by reconciler = real live object count
  reservations:             # small list, in-flight admits not yet observed
    - key: "ns-a/my-bucket"
      expiresAt: "2026-06-29T10:00:30Z"
```

**Effective usage:** `used = confirmed + len(live reservations)`.

### 6.2 Webhook — on `CREATE`

```
key   := (consumerCluster from kcp.io/cluster, gvr)
limit := registry.limitFor(gvr)         # from in-memory policy registry (§5.3)
retry on conflict:
    u := GET QuotaUsage[key]            # create with confirmed=0 if absent
    used := u.status.confirmed + countLive(u.status.reservations)
    if used >= limit:
        DENY  "quota exceeded: N/N <resource> in use for this workspace"
    append reservation {key: ns/name, expiresAt: now + TTL}
    PUT u  (with observed resourceVersion)   # compare-and-swap
    on 409 conflict: retry
    on success: ALLOW
```

Concurrent admits — across replicas or in-flight requests — serialize through etcd's
compare-and-swap on `resourceVersion`. The limit cannot be crossed.

### 6.3 Reconciler — maintaining truth

- **Object observed (ADD):** set `confirmed = trueCount`; drop the reservation whose `key`
  now exists as a live object.
- **Object deleted:** set `confirmed = trueCount` (decrement).
- **Periodic resync / sweep:** set `confirmed = trueCount` from the informer cache; remove
  reservations past `expiresAt`.

### 6.4 Why this is correct

- **No overshoot (R1):** a reservation is only appended when `used < limit`, under CAS;
  concurrent appends conflict and retry. In-flight admits are counted via `reservations`,
  which a live `LIST` would miss — closing the TOCTOU race.
- **No permanent leak:** if an admitted `CREATE` fails downstream (another webhook denies,
  schema error, etc.), its reservation is never folded into `confirmed` and expires after
  TTL. The slot is briefly held (errs strict, never loose), then freed.
- **Restart-safe:** all state is in `QuotaUsage`; on restart the webhook resumes reading it,
  the reconciler rebuilds `confirmed` from the informer.
- **Multi-replica, no leader election for the webhook:** CAS makes concurrent webhook
  replicas safe. (Only the reconciler benefits from leader election.)

### 6.5 TTL choice

`TTL` must exceed the worst-case delay between admission-allow and the object appearing in
the reconciler's informer (create latency + watch propagation), but be short enough to free
slots from failed creates promptly. Start at ~60s; tune from observed propagation latency.
A too-short TTL risks a slot being freed before the object is observed (→ momentary
over-allow); a too-long TTL only delays freeing slots from failed creates (→ stricter than
necessary). Bias toward the longer, strict side.

## 7. Provider-facing API

Mirrors `DependencyRule`'s shape (a `governed` ref analogous to `DependentRef`).

```yaml
apiVersion: quota.opendefense.cloud/v1alpha1
kind: ConsumptionQuota
metadata:
  name: bucket-quota
spec:
  governed:
    apiExportName: s3.example.com    # APIExport in the same workspace as this policy
    group: s3.example.com
    version: v1alpha1
    resource: buckets                # plural; used to build the GVR
  by: Count                          # enum; iteration 1 = Count
  limit: 3                           # default per consumer workspace
  # iteration 2 will add, when by: Sum —
  #   fieldRef: { path: ".status.sizeBytes" }
  #   limit: "1Ti"                   # quantity with units
```

`QuotaUsage` is an internal CRD (not provider-authored); see §6.1.

## 8. RBAC & permissionClaims

Reuses dep-ctrl's static-bootstrap model. No dynamic RBAC at runtime.

- **`permissionClaim` on the quota-ctrl `APIExport`:** `validatingwebhookconfigurations`
  (admissionregistration.k8s.io) — to install webhooks in provider workspaces. Providers
  **accept** it in their `APIBinding`.
- **quota-ctrl workspace RBAC (both binaries):** `apiexportendpointslices` get/list/watch
  (discover the VW URL) + `apiexports/content` on the quota-ctrl `APIExport`.
- **Workspace-resolution RBAC (controller):** `tenancy.kcp.io/workspaces` get/list/watch +
  `system:kcp:workspace:access`, as in dep-ctrl.
- **NEW — provider `APIExport` content read (reconciler):** the accounting reconciler reads
  the governed objects across consumers via the provider's `APIExport` virtual workspace, so
  the provider must grant the quota system read (`get,list,watch`) on that `APIExport`'s
  content. This is the one new bootstrap grant relative to dep-ctrl. *(The webhook itself
  needs no consumer-workspace read RBAC — an improvement over dep-ctrl.)*

## 9. Failure modes

- **Webhook unreachable →** fail-closed (R6): governed `CREATE`s rejected. Mitigate with
  multiple webhook replicas (CAS-safe) and tight admission timeouts.
- **Reconciler down →** `confirmed` goes stale. Webhook still enforces against the last
  known `confirmed` + reservations; reservations accumulate and (via TTL) cap drift. On
  recovery, resync corrects `confirmed`. No overshoot, but possible temporary
  over-strictness if many creates churn while it is down.
- **QuotaUsage store unreachable →** webhook cannot CAS → fail-closed.
- **Policy deleted →** controller removes the webhook (no policy ⇒ no enforcement), mirroring
  dep-ctrl's `handleDeletion`.

## 10. Future improvements (designed-for, not built)

- **Per-consumer overrides (R4):** add `spec.overrides` keyed by consumer logical cluster
  (or a selector) to `ConsumptionQuota`; reconciler resolves the effective limit per
  consumer and writes it into each `QuotaUsage.spec.limit`. The webhook is unaffected (still
  reads one resolved object).
- **Per-namespace scope (R5):** key `QuotaUsage` by `(consumerCluster, namespace, gvr)` and
  intercept with namespace in the admission key. (Note: per-namespace caps invite consumers
  to multiply their effective cap by creating namespaces — usually undesirable for provider
  quotas; per-workspace is the default for good reason.)
- **Iteration 2 — aggregate/property quotas (`sum < 1TB`):** same skeleton; `by: Sum` +
  `fieldRef` + a quantity `limit`. Reservations carry the *value* instead of `1`; `used` is
  a sum. The webhook must additionally intercept **`UPDATE`** (a size change moves the sum),
  reserving the *delta*. Everything else (CAS, reconciler truth, TTL) is unchanged.

## 11. Open questions / to verify before/during implementation

1. **CREATE webhook interception:** dep-ctrl proves a provider-workspace webhook intercepts
   `DELETE` of the exported type across consumers; `CREATE` is symmetric and expected to
   work, but verify with a spike (it is the load-bearing kcp behavior).
2. **Provider VW access for the reconciler:** confirm the cleanest mechanism for granting
   the quota system cross-consumer read on the provider's `APIExport` content (bootstrap
   RBAC vs another path), and that a single watch stream across consumers behaves as
   expected at scale.
3. **Workspace-nesting limitation:** dep-ctrl's resolver only handles direct children of
   `root`. Same limitation inherited here for the provider-workspace path; decide whether to
   lift it (walk the path) as part of this work or carry it forward.
4. **`QuotaUsage` location & lifecycle:** confirm storing accounting objects in the
   quota-ctrl workspace (vs per-provider) and garbage-collection when a consumer unbinds.

## 12. Testing strategy

- **Unit:** CAS reservation logic (concurrent admits, conflict retry, TTL sweep, fold-in),
  field/limit resolution. Table-driven + race detector.
- **Integration (envtest):** webhook admission against a real apiserver — at-limit denial,
  reservation persistence, restart recovery.
- **e2e (kind + kcp, mirroring dep-ctrl's suite):** provider creates a `ConsumptionQuota`;
  consumer creates up to N (succeeds) and N+1 (denied); concurrent burst of creates never
  overshoots; webhook-down ⇒ fail-closed; delete frees a slot.

## 13. Out of scope (iteration 1)

- Aggregate/property quotas (iteration 2).
- Per-consumer overrides; per-namespace scope.
- Quota for non-`APIExport` (built-in) resource types.
- Usage reporting / metrics surface beyond what is needed for enforcement.

## 14. Relationship to dependency-controller

| Aspect | dependency-controller | quota-controller |
|---|---|---|
| Provider config CRD | `DependencyRule` | `ConsumptionQuota` |
| Webhook installed in | provider WS (via permissionClaim) | **same** |
| Verb intercepted | `DELETE` | `CREATE` (iteration 2 adds `UPDATE`) |
| Webhook decision | stateless live `LIST` of dependents | reads `QuotaUsage` (CAS), no consumer `LIST` |
| Webhook RBAC to consumers | broad per-shard read | **none needed** |
| New moving part | — | accounting reconciler + `QuotaUsage` (CAS) |
| Workspace resolution | path → logicalCluster | **same** (same limitation) |
