# ADR-003: Fail closed on unsynced policy and unnamed (generateName) creates

**Status:** Accepted

**Date:** 2026-07-02

**Related:** [ADR-001](./ADR-001-external-cas-quota-enforcement.md) (external CAS enforcement), [design spec](../docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) §7, §11. Arose from the Phase-1 final whole-branch review.

## Context

The admission webhook enforces quotas for governed resources. Two admission-time
edge cases were found to silently weaken the strict, no-overshoot guarantee (spec R1/R2/R6):

1. **Unsynced policy.** The webhook's in-memory limit registry is populated by an
   independent watch of `ConsumptionQuota` that races the controller's installation of
   the per-quota `ValidatingWebhookConfiguration`. Because the VWC is installed *per
   quota*, the webhook only ever receives traffic for a governed GVR that already has a
   `ConsumptionQuota`. Therefore a registry miss does **not** mean "no policy" — it means
   "this replica hasn't synced the policy yet." The original code treated a miss as
   "no quota configured" and **allowed** the create (fail open), opening a window right
   after a new quota is created where governed creates slip through unlimited.

2. **`generateName` (unnamed) creates.** For a `CREATE` using `metadata.generateName`,
   the apiserver assigns `metadata.name` *after* admission, so the admission request's
   name is empty. The reservation key `namespace/name` collapsed to `namespace/` for
   every such request, and the reservation idempotency shortcut matched that single key
   before the limit check — so repeated or concurrent `generateName` creates were all
   admitted against one slot, with no limit check at all.

## Decision

**Fail closed on uncertainty, and give every in-flight create a distinct reservation
identity.**

1. **Registry miss ⇒ deny.** When the webhook has no limit for a governed GVR it is
   receiving traffic for, it denies the create (HTTP 500 / not-allowed) rather than
   allowing it. A policy-scoped webhook never treats "not synced" as "unrestricted."
   Startup is already gated by the readiness probe (no traffic until the initial
   `ConsumptionQuota` list has synced); this covers the post-startup, per-quota race.

2. **Unnamed creates keyed by request UID.** When the admission request has no object
   name (`generateName`), the reservation is keyed by the admission request UID instead
   of `namespace/name`. Each in-flight create thus consumes a distinct reservation and is
   checked against the limit like any named create.

## Consequences

**Positive**
- Closes both silent over-allow / bypass paths; enforcement stays strict for named and
  `generateName` creates alike, including under concurrency.
- Consistent with the system's stated posture (external authority, fail-closed).

**Negative / trade-offs (both err strict — deny/over-hold, never over-allow)**
- A straggler create arriving in the brief window *after* a `ConsumptionQuota` is deleted
  (VWC not yet removed) but *before* the registry reflects the deletion is denied. Bounded
  and self-healing (the controller removes the VWC on deletion).
- A create arriving before a *new* quota has synced into a webhook replica is denied until
  the watch delivers it (seconds); self-healing.
- A `generateName` reservation cannot fold into `confirmed` (the persisted object's key is
  `namespace/<generated-name>`, not the UID), so it lingers until the TTL sweep. During
  that window usage is counted slightly high (over-strict) for the affected consumer;
  corrected automatically at TTL. Never over-allows.
