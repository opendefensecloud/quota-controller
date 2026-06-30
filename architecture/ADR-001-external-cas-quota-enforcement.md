# ADR-001: External CAS webhook for provider-defined consumption quotas

**Status:** Accepted
**Date:** 2026-06-29
**Related:** [`docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md`](../docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md)

## Context

Service providers in kcp expose APIs via `APIExport`s; consumer workspaces bind to them and
create instances of the exported types in their own workspaces. Providers need to cap how
many instances each consumer workspace may create.

Three requirements, fixed during design, constrain the solution space sharply:

1. **Strict — no overshoot, ever.** The limit may never be exceeded, even momentarily.
2. **Cap admitted API objects.** The `CREATE` must be rejected at the API; nothing is
   stored once at the limit. (Not merely "actually-provisioned" resources.)
3. **Adversarial consumers, no RBAC control.** Consumers are full admins of their own
   workspaces and may actively try to bypass the quota.

We already operate the [`dependency-controller`](https://github.com/opendefensecloud/dependency-controller),
which installs a `ValidatingWebhookConfiguration` in the *provider* workspace to intercept
operations on the exported type across all consumer workspaces. That topology is proven and
reusable.

## Decision

Enforce quotas with an **external, stateful admission webhook installed in the provider
workspace** (reusing the dependency-controller topology), holding authoritative accounting
in a **persisted `QuotaUsage` object** and reserving slots via **optimistic-concurrency
(compare-and-swap)** on `resourceVersion`.

- The webhook intercepts `CREATE` of the governed type. It reads the limit from an in-memory
  registry of `ConsumptionQuota` policies and CAS-reserves a slot in the `QuotaUsage` object
  for `(consumerCluster, gvr)`. Effective usage = `confirmed + live reservations`.
- An accounting reconciler watches the governed objects across consumers via the provider's
  `APIExport` virtual workspace and maintains the true `confirmed` count, folding in and
  TTL-sweeping reservations.
- The webhook fails closed.

The authoritative state (`QuotaUsage`, policies, limits) lives **outside** any consumer
workspace, so adversarial consumers cannot tamper with it.

## Alternatives considered

- **Injected `ResourceQuota` in the consumer workspace** — kcp-native and genuinely atomic
  (runs inside the apiserver). *Rejected:* the object lives in the consumer's own workspace;
  an adversarial workspace-admin would edit or delete it, and reconciliation cannot close
  the tamper window. (Would be the simplest option under a trusted model or if the platform
  controlled consumer-workspace RBAC — revisit if the trust model changes.)
- **Stateless live-count webhook** (count on each admission, like dep-ctrl's deletion
  check) — *Rejected:* a live `LIST` races against not-yet-persisted creates (TOCTOU);
  concurrent creates both pass.
- **Actuation-gating** (admit the object; the provider's controller refuses to provision
  beyond N) — *Rejected:* caps provisioned resources, not admitted objects; the `CREATE` is
  not rejected at the API (violates requirement 2).
- **In-memory single-writer counter** (instead of persisted CAS) — viable, but requires a
  single active writer / leader election and rebuilds state on restart. The persisted-CAS
  variant was chosen for HA (multiple webhook replicas, no leader election) and
  restart-safety.

## Consequences

**Positive**
- Tamper-proof against adversarial consumers (authority lives outside their workspace).
- Strict no-overshoot via CAS reservations that account for in-flight admits (closes TOCTOU).
- Reuses the proven dependency-controller skeleton (topology, RBAC, webhook installer).
- The webhook needs **no** broad cross-workspace read RBAC (an improvement over dep-ctrl);
  only the reconciler reads across consumers.
- Multiple webhook replicas without leader election; survives restarts.
- Generalizes to iteration-2 aggregate/property quotas (`sum < 1TB`) with the same skeleton.

**Negative / costs**
- **Not truly atomic with the apiserver write** the way native `ResourceQuota` is.
  Strictness relies on the webhook being the sole serialization point + CAS + fail-closed.
- **Fail-closed:** while the webhook is unavailable, governed `CREATE`s are rejected.
  Mitigated by multiple replicas and tight timeouts.
- New moving parts vs dep-ctrl: the accounting reconciler, the `QuotaUsage` CRD, and a TTL
  to tune for the reservation lifecycle.
- One new bootstrap grant: the reconciler needs read access to the provider's `APIExport`
  content (cross-consumer watch).
