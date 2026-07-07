# Improvement backlog

Findings from a review on 2026-07-04 comparing this project's permission concept
with `opendefensecloud/dependency-controller` and analyzing how the accounting
fan-out scales with the number of governed APIExports. Items are ordered by
leverage within each section. See `architecture/ADR-001-external-cas-quota-enforcement.md`
and `docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md` for the
underlying design; spec §"Open questions" already anticipates several of these.

## Scaling / performance (accounting fan-out)

The webhook hot path is O(1) per admission regardless of quota count. All
scaling cost sits in the controller's accounting manager, which runs one
multicluster-runtime sub-manager per distinct governed export
`(group, resource, identityHash)`.

1. **Metadata-only informers for Count quotas.** Sub-managers currently watch
   full `unstructured.Unstructured` objects, so the cache holds complete JSON of
   every governed object across every consumer. Count-based quotas only need
   existence plus the `kcp.io/cluster` annotation — switch to
   `PartialObjectMetadata` watches. For Phase-2 `by: Sum` quotas, use cache
   transform functions to strip `managedFields`/`status` instead. This is the
   single biggest memory lever and a small, local change in
   `cmd/controller/accounting.go`.
2. **Merge sub-managers per (provider, APIExport) instead of per governed GVR.**
   A provider governing N resources of one export currently runs N apiexport
   providers, caches, and sweep tickers against the same virtual workspace. One
   sub-manager per export hosting N controllers shares that plumbing.
3. **Jitter startup and re-lists.** On leader restart every sub-manager re-lists
   at once — a thundering herd against kcp and a window of accounting lag
   (fail direction is over-strict, so correctness holds, but it is user-visible).
4. **Instrument before optimizing further.** Expose: live accounting-set count,
   cache object counts, watch reconnect rate, `QuotaUsage` CAS conflict rate,
   sweep duration. Watch streams scale with exports × shards (the apiexport
   provider watches the VW wildcard), not with consumers — verify that holds in
   practice. Only if a single leader becomes the ceiling, shard governed exports
   across controller replicas by key hash; do not build that speculatively.

## Permission / RBAC

5. **Verify least-privilege RBAC under the real identities.** The sysdemo
   deployment authenticated both components as `kcp-admin`, so the
   `quota-controller` and `quota-controller-webhook` ClusterRoles are rendered
   and reviewed but have never been exercised. Redeploy with the actual
   ServiceAccount identities and run the e2e scenarios to confirm the grants are
   sufficient (missing verbs would currently surface only in production).
6. **Surface a missing provider-side grant as an explicit error.** If a provider
   accepts the permissionClaim but forgets to apply `provider-rbac.yaml`, the
   symptom is silently stalled accounting. Add a `ConsumptionQuota` status
   condition and an Event when accounting reads fail with Forbidden.
7. **Automate the kcp RBAC bootstrap.** Avoid dependency-controller's
   manual-fixture pattern: ship the bootstrap grants as chart-rendered manifests
   or a documented installer step, so a new environment cannot skip them.

## Inherited limitations / housekeeping

8. **Path-aware workspace resolver.** Inherited from dependency-controller:
   provider workspaces must be direct children of `root`; nested workspace paths
   fail to resolve.
9. **`QuotaUsage` garbage collection** when a consumer workspace unbinds the
   provider's APIExport (spec open question 4).
10. **Commit the Phase-1 implementation.** At current HEAD only the design docs
    are committed; the reviewed, live-verified implementation exists only as
    working-tree state. Also remove the stray compiled `controller` binary from
    the repo root and add it to `.gitignore`.

## Sibling repo: dependency-controller

11. **Backport the scoped-RBAC pattern.** dep-ctrl's webhook holds a wildcard
    `*/* get,list` bound per shard in `system:admin` — the widest privilege on
    the platform, applied manually via `system:masters`. This project's
    per-provider opt-in grant plus own-ledger pattern shows how to narrow it,
    at the cost of trading stateless live LISTs for a maintained index. Worth an
    issue/ADR in that repo rather than a config tweak.
