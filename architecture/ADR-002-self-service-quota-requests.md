# ADR-002: Self-service quota requests with maximalPermissionPolicy-protected status

**Status:** Accepted

**Date:** 2026-07-01

**Related:** [ADR-001](./ADR-001-external-cas-quota-enforcement.md) (enforcement mechanism it builds on), [design spec](../docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md).

## Context

ADR-001 established provider-defined, strictly-enforced consumption quotas via an external CAS
webhook. The team then required a self-service layer on top:

- Consumers can **request** a higher cap for a resource kind.
- Providers can **approve/reject** pending requests **in their own workspace**; requests
  under a provider-set ceiling should be auto-approved.
- Consumers can **see** their current cap and any request's state.
- Consumers must not be able to abuse this to raise their own cap.

The enforcement authority already lives outside consumer workspaces (ADR-001), so consumers
have no path to the enforced limit today. But a *self-service* surface must, by definition, be
visible and writable to consumers — which reintroduces a tamper surface that must be contained.

## Decision

Add a request/approval workflow around the ADR-001 core, changing only how the webhook
resolves the *effective* limit — the CAS enforcement mechanism is untouched.

- **Two quota-system `APIExport`s:** a *provider-facing* one (`ConsumptionQuota` policy +
  `QuotaGrant` decisions) and a *consumer-facing* one (`QuotaClaim`). `QuotaUsage` accounting
  stays internal to the quota-ctrl workspace.
- **Request in `spec`, decision in `status`, claims are controller-created.** The controller
  pre-creates one `QuotaClaim` per `(consumer, governed resource)`. Consumers may **only
  `update` `spec.requestedLimit`** and **read** `status`; the consumer-facing export's kcp
  **`maximalPermissionPolicy`** grants `get/list/watch/update/patch` on `quotaclaims` and
  `get/list/watch` on `quotaclaims/status`, but **omits `create` and `delete`**. So consumers
  can ask (by editing a pre-created claim) but cannot spawn/delete claims or self-approve. RBAC
  distinguishes `create` from `update` per (sub)resource, so this is expressible exactly.
- **Resource identity (kcp convention).** Every governed-resource reference carries the identity
  tuple `(group, resource, identityHash)` — resolved by the controller from the governed
  `APIExport.status.identityHash` and stamped onto the policy status and derived grants/claims —
  matching how kcp addresses exported resources (`resource.group:identityHash`) and
  `PermissionClaim.identityHash`. `QuotaUsage` and the webhook registry are keyed by it, so two
  providers exporting the same `group/resource` never collide.
- **Provider approves in their workspace** by editing `QuotaGrant` objects (one per
  consumer×resource) that live in the provider workspace. A provider-set `autoApproveCeiling`
  on `ConsumptionQuota` lets the controller write an `Approved` grant automatically for
  requests at/below the ceiling; larger requests become `Pending`.
- **A request/approval controller** bridges the two sides: pre-creates `QuotaClaim`s for
  visibility, turns claims into grants (auto or pending), and writes decisions back to
  `QuotaClaim.status`.

**Abuse prevention is layered:** (1) enforcement reads *only* provider-workspace objects, so no
consumer-workspace object can change the enforced limit; (2) `maximalPermissionPolicy` keeps
the consumer-visible status trustworthy (no forged approvals); (3) the auto-approve ceiling
lives in the provider workspace, out of consumer reach.

## Alternatives considered

- **Fold the `QuotaClaim` type into each provider's own `APIExport`** (instead of a shared
  consumer-facing export). *Rejected:* duplicates the machinery and the `maximalPermissionPolicy`
  into every provider and invites misconfiguration; a shared export defines it once.
- **Overrides/pending requests as fields on `ConsumptionQuota`** (instead of separate
  `QuotaGrant` objects). *Rejected:* one object accumulates every consumer's requests →
  large objects, noisy edits, coarse RBAC. Separate grants scale and have clean lifecycle.
- **On-demand consumer visibility** (consumer must create a `QuotaClaim` to see the limit).
  *Rejected* for UX and now for control: chosen model pre-creates claims so the current limit is
  always visible, and consumers are denied `create` so the claim set stays provider-controlled.
  Consequence: proactive-creation discovery (which `(consumer, resource)` pairs need a claim)
  becomes load-bearing rather than a convenience.
- **Manual-approval only** (no ceiling). *Deferred into* the chosen design as the `ceiling
  omitted` case; auto-approve under a ceiling is the default self-service behavior.
- **Enforce the cap via `maximalPermissionPolicy` / RBAC directly.** *Rejected as infeasible:*
  RBAC cannot count instances; it can only gate verbs. The CAS webhook remains the counter.

## Consequences

**Positive**

- Full self-service (request → auto/manual approval → enforced) with provider-local approval.
- Tamper-proof: enforcement never trusts consumer-workspace state; status is integrity-protected.
- Enforcement core (ADR-001) is unchanged — only effective-limit resolution gains grants.
- Generalizes to iteration-2 aggregate quotas (grants/claims/ceilings carry quantities).

**Negative / costs**

- Consumers now bind to a quota-system export — the original "consumers never touch the quota
  system" property is gone (inherent to self-service).
- New moving parts: a second `APIExport`, the `QuotaClaim`/`QuotaGrant` types, and the
  request/approval controller (with cross-workspace writes via both quota VWs).
- Correct `maximalPermissionPolicy` (update-only, no `create`/`delete`, read-only `status`) is
  now security-relevant and must be verified.
- Because consumers cannot `create` claims, proactive `QuotaClaim` creation is the *only* path
  to a claim, so discovering (consumer, governed-resource) pairs is load-bearing (a consumer
  briefly cannot request until its claim exists).
- Identity resolution adds a dependency on the governed `APIExport.status.identityHash` being
  populated before quotas are wired, and a decision about identity rotation.
