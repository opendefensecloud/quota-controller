# Default `quota-consumer` binding for account workspaces — Design

**Status:** Approved (brainstorming output, user-validated)

**Date:** 2026-07-09

**Related:** [ADR-002](../../../architecture/ADR-002-self-service-quota-requests.md)
(self-service), [ADR-005](../../../architecture/ADR-005-claim-discovery-via-service-export-vw.md)
(claim discovery), the [Platform Mesh UI design](./2026-07-09-quota-platform-mesh-ui-design.md).

## 1. Problem

A consumer workspace only gets the self-service surface once it binds the
`quota-consumer` APIExport (`root:cloud-api-system`). Today that binding is
created by hand per account (the sysdemo wiring bound three acme accounts
individually). We want every **account**-type workspace to bind `quota-consumer`
automatically at creation, without a manual step.

## 2. Decision

Add one entry to the platform's `PlatformMesh` CR (`platform-mesh-system/platform-mesh`):

```yaml
spec:
  kcp:
    extraDefaultAPIBindings:
    - export: quota-consumer
      path: root:cloud-api-system
      workspaceTypePath: root:account
```

kro reconciles `extraDefaultAPIBindings` into the target WorkspaceType's
`spec.defaultAPIBindings` (verified live: the existing
`dependencies.opendefense.cloud → root:provider` entry lands in the `provider`
type's `defaultAPIBindings`). Targeting the base **`root:account`** type means
every account platform-wide auto-binds `quota-consumer`, and `acme-account`
inherits it through `extends: {name: account, path: root}`. The binding needs no
permission claims (the export offers none; existing `quota-consumer` bindings
have an empty `permissionClaims`).

**Why this mechanism.** Alternatives rejected:

- *Patch the `account`/`acme-account` WorkspaceType `defaultAPIBindings`
  directly* — kro owns that field and would reconcile the manual entry away.
- *Live-patch the `PlatformMesh` CR* — it is Flux-managed
  (`kustomize.toolkit.fluxcd.io/name: platform-mesh`, applied by
  `kustomize-controller` from the `development/kind-local` GitOps source); Flux
  would revert it.
- *A custom controller that watches for new accounts and creates the binding* —
  more code and a new failure mode to own, for something the platform already
  supports declaratively.

## 3. Codification

The durable change is a one-line addition to `extraDefaultAPIBindings` made in
the **platform's GitOps source** (the repo Flux reconciles the `PlatformMesh` CR
from) — it is a platform-config change, not a quota-repo change. This repo's
only edit is **documentation**: record the exact entry and its rationale in
`config/platform-mesh/README.md` and a one-line pointer in the getting-started
portal section, so anyone wiring the add-on into a Platform Mesh install knows
to add it.

## 4. Scope

**In scope:** the documented `extraDefaultAPIBindings` entry (targeting
`root:account`) and where to apply it.

**Out of scope (user decisions):**

- **No backfill of existing accounts.** `defaultAPIBindings` applies only at
  workspace creation; the ~8 current acme accounts are left as-is (several are
  already bound). New accounts pick it up automatically.
- **No ADR** for this decision at this time.
- No quota-controller code change; no change to `config/kcp` (the
  `quota-consumer` export already exists and permits binding via its MPP).
- Editing the platform GitOps source itself (a separate repo) is out of this
  repo's scope — we provide the snippet.

## 5. Testing / verification

No unit/e2e code is added (documentation only). Acceptance is live: after the
entry is added and kro reconciles, the `account` WorkspaceType's
`spec.defaultAPIBindings` includes `quota-consumer`, and a **newly created**
account workspace shows a `quota-consumer` APIBinding
(`kubectl get apibinding -o name`) without manual action. Existing accounts are
unaffected (expected, per §4).
