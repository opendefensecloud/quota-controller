# ADR-004: Read governed APIExport metadata via a direct workspace client, not the quota-provider VW

**Status:** Accepted

**Date:** 2026-07-02

**Related:** [ADR-001](./ADR-001-external-cas-quota-enforcement.md) (external CAS enforcement), [design spec](../docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) §5, §7.2. Arose from live deployment on the sysdemo cluster.

## Context

The controller connects to provider workspaces through the **quota-provider APIExport
virtual workspace** (VW) — the correct client for the `ConsumptionQuota` it exports and (via
permissionClaim) the `ValidatingWebhookConfiguration` it installs. To wire enforcement it must
also read, in the provider workspace:

- the governed service `APIExport` (`status.identityHash`, in the `ConsumptionQuota` controller), and
- that service export's `APIExportEndpointSlice` (in the accounting reconciler, to locate the
  service VW and watch governed objects across consumers — which keeps `QuotaUsage.confirmed`
  accurate).

These are `apis.kcp.io` types. The initial implementation read them through the VW client, which
fails at runtime: the APIExport VW does **not** serve `apiexports`, `apiresourceschemas`, or
`apiexportendpointslices`. A `permissionClaim`-only workaround was tried and **rejected on-cluster**:
the VW serves `apiexports`/`apiresourceschemas` when claimed but still does **not** serve
`apiexportendpointslices`, so the accounting reconciler never starts, `confirmed` stays `0`, and —
critically — once the 60s reservation TTL expires, quota enforcement **bypasses** (verified: a 4th
create was allowed under a limit of 3 after the TTL). The immediate reservation-based deny masked
this in short tests.

## Decision

Read all governed-APIExport metadata (`apiexports`, `apiresourceschemas`,
`apiexportendpointslices`) through a **direct workspace-scoped client** — a `rest.Config` built
from the front-proxy base with `Host = <base>/clusters/<providerCluster>` — not through the VW and
not via permissionClaims. The VW client is retained only for what the VW actually serves:
`ConsumptionQuota` get/update/status and the `ValidatingWebhookConfiguration` install.

## Consequences

**Positive**
- Durable enforcement: the accounting reconciler starts, `confirmed` tracks live objects, and the
  limit holds past the reservation TTL (verified live: 4th create denied 138s after the first
  denial; `confirmed` 3→2 on delete).
- One uniform reason for the split (VW for the quota API, direct for kcp system metadata); no extra
  claims on provider bindings.

**Negative / follow-up**
- The controller needs read access to `apiexports`/`apiresourceschemas`/`apiexportendpointslices`
  in provider workspaces. In the sysdemo demo it runs as `kcp-admin`; a scoped-down production
  deployment must add explicit bootstrap RBAC granting those reads (dep-ctrl style) — tracked as a
  follow-up, not implemented in Phase 1.
- A second client path exists (VW + direct). The `APIExportReader` field and `cmd/controller`
  carry a do-not-revert comment pointing here, because reverting to the VW client silently
  reintroduces the post-TTL bypass.
