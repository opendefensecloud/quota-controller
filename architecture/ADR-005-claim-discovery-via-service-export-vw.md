# ADR-005: Claim discovery via consumers' APIBindings in the governed service export's virtual workspace

**Status:** Accepted

**Date:** 2026-07-08

**Related:** [ADR-002](./ADR-002-self-service-quota-requests.md) (controller-created claims make discovery load-bearing), [ADR-004](./ADR-004-direct-client-for-governed-apiexport-reads.md) (the per-provider grant this reuses), [design spec](../docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) §14.2, [1b spike notes](../docs/superpowers/specs/2026-07-08-1b-spike-notes.md) (verification evidence, kcp v0.32.3).

## Context

ADR-002 made `QuotaClaim`s **controller-created**: consumers can only `update` a pre-created claim
(the consumer-facing export's `maximalPermissionPolicy` omits `create`/`delete`). Pre-creation is
therefore the *only* path to a claim, so the controller must discover every
`(consumer workspace, governed resource)` pair — ideally the moment a consumer binds a governed
service export, before it creates its first governed object (R11: the current limit should be
visible immediately).

The 1a accounting reconciler already connects to each governed service export's **virtual
workspace** (VW) to watch governed objects across consumers (ADR-004's per-provider
`apiexports/content` opt-in grant). Whether that same VW also reveals *who is bound* — at binding
time, before any governed object exists — was unverified kcp behavior (spec §14.2), settled by the
1b Task-1 spike against kcp v0.32.3.

The spike confirmed: the APIExport VW **always serves `apibindings.apis.kcp.io`** (read-only),
statically filtered to the bindings that reference *this* export — kcp's APIBinding admission
stamps the `internal.apis.kcp.io/export` label at create time, so a fresh consumer's binding is
listable via `{vwURL}/clusters/*` (annotation `kcp.io/cluster` = consumer logical cluster) while
the consumer workspace contains zero governed objects.

## Decision

**Discover claim targets by listing consumers' `APIBinding`s through the governed service
APIExport's virtual workspace.** Each per-governed-export accounting sub-manager runs a periodic
sweep (its existing `resync` interval) that:

1. lists `APIBindingList` at `{serviceVW}/clusters/*` using the sub-manager's existing VW config,
2. maps bindings of the governed export to consumer logical clusters (`kcp.io/cluster` annotation),
3. diffs against the previous round: new consumer → ensure a `QuotaClaim` exists for
   `(consumer, governed resource)`; disappeared consumer (unbind) → delete the pre-created claim.

Claim GC thereby mirrors discovery exactly. `QuotaGrant`s are provider-owned decision records and
are never touched by discovery.

No new access is introduced: the reads ride the ADR-004 per-provider grant the accounting
reconciler already holds, and the VW serves APIBindings to the export owner read-only, so the
discovery path cannot mutate consumer bindings even if compromised.

## Alternatives considered

- **`apibindings` permissionClaim on the quota-consumer export.** *Rejected:* permissionClaims
  require per-consumer acceptance in each `APIBinding` — acceptance friction for every consumer,
  and an adversarial consumer can simply decline the claim and become invisible to discovery while
  still using the service. Also unnecessary: the service VW serves the same signal without any claim.
- **Claim-on-first-object** (create the `QuotaClaim` when the consumer's first governed object is
  admitted). *Rejected:* no visibility before first use — a consumer could not see its limit or
  request a raise until after it started consuming, violating R11; also couples claim lifecycle to
  webhook traffic instead of binding state.
- **Provider-maintained consumer list** (provider declares its consumers in the `ConsumptionQuota`
  or a side object). *Rejected:* manual bookkeeping drifts from reality (late additions, forgotten
  removals); kcp already maintains the authoritative binding set — duplicating it invites skew.

## Consequences

**Positive**
- Discovery reuses the per-governed-export accounting VW config and RBAC — no new grants, no
  permissionClaims, nothing a consumer must accept (or can decline).
- Binding-time visibility: a consumer gets its pre-created claim (and thus sees its limit) before
  creating any governed object; unbind cleans the claim up via the same diff.
- The signal is authoritative (kcp's own binding state, admission-stamped label), not inferred
  from traffic.

**Negative / trade-offs**
- A consumer briefly has no claim between binding and the next discovery sweep — bounded by the
  accounting `resync` interval (list-based sweep, not a watch, in the first implementation).
- Discovery is per-governed-export: a provider whose export the controller does not yet govern
  (no `ConsumptionQuota`/no ADR-004 grant) is invisible — consistent with the opt-in model, but it
  means claim pre-creation starts only once the provider is fully onboarded.
- Relies on the VW's always-serve-APIBindings behavior (verified on kcp v0.32.3; a fallback —
  watching bindings via an `apibindings` permissionClaim — remains possible if a future kcp
  removes it).
