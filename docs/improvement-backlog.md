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

8. **~~Path-aware workspace resolver~~ (resolved: not applicable).** The
   "provider workspaces must be direct children of `root`" limitation belongs
   to dependency-controller's resolver and was inherited into this backlog
   speculatively. This repo has no path-resolution code: consumer identifiers
   are opaque logical-cluster strings (hashed in `selfservice.GrantName`, with
   an explicit nested-path test), and APIExport lookups go through
   endpoint-slice URLs. Verified live: `root:providers:s3` (two levels deep)
   runs the full quota stack. The item remains relevant to
   dependency-controller only.
9. **`QuotaUsage` garbage collection** when a consumer workspace unbinds the
   provider's APIExport (spec open question 4).
10. **Field-level/action-level visibility control for platform-mesh generic-detail-view.** The `generic-detail-view` renders a non-configurable Delete button on QuotaClaims that the consumer MPP denies (fails safe to /error/403); upstream support for field-level or action-level visibility hints would close the "UI must not offer what the API denies" gap fully and obviate the need for downstream error-page UX.
11. **Commit the Phase-1 implementation.** At current HEAD only the design docs
    are committed; the reviewed, live-verified implementation exists only as
    working-tree state. Also remove the stray compiled `controller` binary from
    the repo root and add it to `.gitignore`.

## Platform-Mesh portal edit path

13. **Upstream: generic edit form coerces unset values to `""` and submits the
    whole object.** `portal-ui-lib`'s `create-resource-modal` builds its form
    from `detailView.fields` (`buildInitialValues`: `getResourceValueByJsonPath(...) ?? ''`)
    and `detail-view.component.update()` submits `{...resource}` including
    `status`. A nullable numeric field (`*int32`) then serialises as `""` and the
    generated GraphQL `Int` type rejects it (`Expected type "Int", found ""`),
    breaking the whole edit. We work around it by keeping every nullable numeric
    field out of the editable `detailView`/`createView` (list-only). A field-level
    read-only hint, or null-not-`""` coercion for non-string scalars, would remove
    the workaround — worth an upstream issue (relates to item 10).
14. **No portal path to set `spec.grantedLimit`.** Because of item 13 the grant
    `detailView`/`createView` no longer expose `grantedLimit`/`requestedLimit`, so
    a provider approves by editing `spec.decision` only; the enforced number is a
    `kubectl` step (docs/getting-started.md). Revisit once the portal can render a
    numeric field that round-trips a null safely.
15. **Structured surfacing of an incomplete approval (deferred).** An Approved
    grant with no `spec.grantedLimit` is surfaced today via the claim/grant
    `reason` (`ReasonApprovedNoLimit`) and claim `phase: Pending` — visible in the
    portal and `kubectl`. A machine-readable `QuotaGrantStatus.Conditions` entry
    (`Effective=False`) plus an Event would be cleaner but needs a new grant
    status field, which means regenerating and redeploying the grant
    `APIResourceSchema` to the live kcp provider APIExport (schemas are immutable;
    the controller cannot persist an unknown status field otherwise). Do it when a
    grant-schema bump is happening anyway.

## Sibling repo: dependency-controller

12. **Backport the scoped-RBAC pattern.** dep-ctrl's webhook holds a wildcard
    `*/* get,list` bound per shard in `system:admin` — the widest privilege on
    the platform, applied manually via `system:masters`. This project's
    per-provider opt-in grant plus own-ledger pattern shows how to narrow it,
    at the cost of trading stateless live LISTs for a maintained index. Worth an
    issue/ADR in that repo rather than a config tweak.
