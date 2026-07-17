# Quota visibility in the Platform Mesh UI — Design

**Status:** Approved (brainstorming output, user-validated)

**Date:** 2026-07-09

**Related:** [1b design spec](./2026-06-29-kcp-consumption-quota-design.md) (the QuotaClaim/QuotaGrant workflow this exposes), [ADR-002](../../../architecture/ADR-002-self-service-quota-requests.md), live exploration of `sysdemo-local` (2026-07-09).

## 1. Problem

Iteration 1b made quotas self-serviceable via `QuotaClaim` (consumer) and `QuotaGrant`
(provider), but nothing is visible in the Platform Mesh portal: no nav node, no claim
view, no approval view. On the live `sysdemo-local` platform the real provider
(`root:providers:s3`) already enforces a `ConsumptionQuota` (defaultLimit 4) against the
acme org accounts — three accounts sit exactly at their bucket limit — yet consumers
cannot see their limit or request a raise anywhere, and providers cannot see or approve
grants. Denials surface only as raw admission errors.

Platform Mesh renders provider UIs declaratively: a **`ContentConfiguration`**
(`ui.platform-mesh.io/v1alpha1`) carries a Luigi config fragment describing nav nodes and
generic list/detail/create views for a resource type. The `extension-manager-operator`
validates it (via the `core.platform-mesh.io` APIExport VW) and the portal merges the
result into each org's navigation, filtered by the org's bound APIExports via the
`ui.platform-mesh.io/content-for: <export>` label. The `s3-buckets` ContentConfiguration
in `root:providers:s3` is the working exemplar.

## 2. Decision

Host **two ContentConfigurations in `root:cloud-api-system`** — the workspace that owns
both quota APIExports — following the platform convention that content lives with the
export it describes:

- `quota-claims`, labeled `ui.platform-mesh.io/content-for: quota-consumer` → org
  accounts that bind `quota-consumer` get the consumer surface.
- `quota-grants`, labeled `ui.platform-mesh.io/content-for: quota-provider` → provider
  workspaces that bind `quota-provider` get the provider surface.

`root:cloud-api-system` must bind `core.platform-mesh.io` (export path
`root:platform-mesh-system`) to host the type — one new APIBinding.

**Alternatives rejected:** per-provider hosting of the claim CC in each provider
workspace (duplicates content, misattributes ownership — the claim UI is generic across
providers); portal-side or custom-web-component configuration (bypasses the declarative
mechanism that already works).

**Out of scope (user decision):** a `ProviderMetadata` marketplace tile for the quota
service; editing flows beyond the two identified below; any portal code changes.

## 3. ContentConfiguration contents

Both files are modeled structurally on the `s3-buckets` exemplar: `spec.inlineConfiguration`
(`contentType: json`) carrying `luigiConfigFragment.data.nodes`, resource identity as the
flat `resourceDefinition` block with the **underscored group** `quota_opendefense_cloud`,
and the generic web components (`/assets/platform-mesh-portal-ui-wc.js#generic-list-view`
/ `#generic-detail-view`, `webcomponent.selfRegistered: true`). Labels carry
`ui.platform-mesh.io/content-for` (as above) and `ui.platform-mesh.io/entity:
core_platform-mesh_io_account` (same account-entity scoping as the exemplar).

### 3.0 Dual-role disambiguation (design constraint)

A workspace can hold BOTH surfaces at once: every provider can also be a consumer
(live proof: `root:providers:s3` consumes `arc.opendefense.cloud` from
`root:providers:odd`). Node labels must therefore encode *direction*, never a bare
"Quotas". Convention used throughout:

- Consumer surface = **"My quotas"** — limits that apply to THIS workspace and the
  requests it makes (claims).
- Provider surface = **"Quota policies"** (the caps I define for my service) and
  **"Quota approvals"** (requests from MY consumers awaiting or holding my decision —
  grants).

All three nodes share one "Quota" category, so a dual-role workspace reads:
*My quotas / Quota policies / Quota approvals* — self-explanatory without knowing the
CRD names. Icons reinforce direction (e.g. gauge for "My quotas", shield-check for
approvals). Additionally, the claims list carries a governed-resource column so a
consumer of several services sees which service each claim caps.

### 3.1 `quota-claims` (consumer surface)

- Nav node: `pathSegment: my-quotas`, label **"My quotas"**, category (id `quota`, label
  "Quota", collapsible group), entityType `main.core_platform-mesh_io_account.namespace`
  — note `QuotaClaim` is **cluster-scoped** (`scope: Cluster`, `namespace: null` in the
  resourceDefinition), unlike the namespaced Bucket exemplar; the implementation verifies
  the generic components handle cluster scope (the resourceDefinition block has a `scope`
  field, so this is expected to work).
- `listView` fields: Name (`metadata.name`), Resource (`spec.governed.resource`), Phase
  (`status.phase`), Effective limit (`status.effectiveLimit`), Requested
  (`spec.requestedLimit`), Reason (`status.reason`).
- `detailView` fields: the list fields plus Granted (`status.grantedLimit`), governed
  resource (`spec.governed.resource`, `spec.governed.group`), Last transition
  (`status.lastTransitionTime`).
- **No `createView`.** Consumers cannot create claims (maximalPermissionPolicy); the UI
  must not offer what the API denies.
- Detail child node with `defineEntity`/`graphqlEntity` wiring, mirroring the exemplar's
  bucket detail route (group `quota_opendefense_cloud`, kind `QuotaClaim`).

### 3.2 `quota-grants` (provider surface)

- Two top-level nodes under the same "Quota" category (see §3.0 naming):
  - **"Quota policies"** (`pathSegment: quota-policies`): `consumptionquotas` — listView
    Name, Governed resource (`spec.governed.resource`), Default limit
    (`spec.defaultLimit`), Auto-approve ceiling (`spec.autoApproveCeiling`); detailView
    adds `status.identityHash` and `spec.by`.
  - **"Quota approvals"** (`pathSegment: quota-approvals`): `quotagrants` — listView
    Name, Consumer (`spec.consumer`), Requested (`spec.requestedLimit`), Granted
    (`spec.grantedLimit`), Decision (`spec.decision`), Reason (`spec.reason`);
    detailView adds the governed tuple and `status.appliedAt`, `status.phase`.
  - `createView` on grants only (providers may create grants proactively — allowed by
    the API): Consumer, GovernedRef, governed tuple, GrantedLimit, Decision.
- Both cluster-scoped, same `scope: Cluster` note as above.

### 3.3 The edit-path unknown (empirical gate)

The two actions that make the surfaces *interactive* — a consumer editing
`spec.requestedLimit`, a provider flipping `spec.decision` to `Approved`/`Rejected` — need
the generic detail view to support field editing. The exemplar only demonstrates
list/detail/create. **The implementation verifies this empirically on sysdemo** (portal
`CONTENT_CONFIGURATION_VALIDATOR_API_URL` and the `generic-detail-view` component are the
places to probe). Outcomes:

- Editing works → declare the editable fields and done.
- Editing does not work → the views stay read-only (still the core "visible" goal), and
  `docs/getting-started.md` gains the two one-line `kubectl patch` commands next to the
  UI walkthrough as the action path. No custom UI work in this iteration.

## 4. Repo placement

New `config/platform-mesh/` directory (own `kustomization.yaml`):

- `apibinding-core-platform-mesh.yaml` — the `root:cloud-api-system` binding (documented
  as "apply in the quota-ctrl workspace").
- `contentconfiguration-quota-claims.yaml`
- `contentconfiguration-quota-grants.yaml`
- `README.md` (three lines: what this is, that it is an optional add-on for Platform
  Mesh installations, where to apply it).

Kept separate from `config/kcp/` because the quota system works without Platform Mesh;
this directory is the optional UI add-on. `docs/getting-started.md` gains a short
"Portal visibility (Platform Mesh)" section.

## 5. Live wiring on sysdemo (verification environment)

1. Apply `config/platform-mesh/` to `root:cloud-api-system`.
2. Create `quota-consumer` APIBindings in the three s3-bound acme accounts
   (`root:orgs:acme:demo`, `root:orgs:acme:quota-test`, `root:orgs:acme:tui-test-2`).
   Claims (`qc-buckets-<id8>`) appear within one discovery sweep; all three accounts are
   at their bucket limit, so the request-a-raise flow demos immediately.
3. Delete the redundant fixture workspaces `root:quota-s3-provider` and
   `root:quota-consumer1` (created by the 1b deploy; disconnected from the portal).
4. Acceptance: both ContentConfigurations reach `Ready`/`Valid` conditions
   (extension-manager); the portal at `https://acme.sysdemo.api.opendefense.cloud`
   (Keycloak realm `acme`, user `cyrill.berg@opendefense.cloud`) shows "Quotas" in the
   org accounts with live claim state; `root:providers:s3`'s view shows the policy and
   grants. Side effect worth recording: this is the first time a real
   portal-authenticated consumer exercises the maximalPermissionPolicy path.

## 6. Doc correction folded in

`docs/improvement-backlog.md` item 8 claims provider workspaces must be direct children
of `root` (inherited from dependency-controller). Code inspection (no path-resolution
code anywhere; consumer identifiers are opaque/hashed with an explicit 7-segment-path
test) and live evidence (`root:providers:s3`, two levels deep, fully functional) disprove
it for this repo. Rewrite the item to state the limitation applies to
dependency-controller only.

## 7. Testing

- `kubectl kustomize config/platform-mesh/` renders; the two CC JSON fragments pass the
  extension-manager validator (live `Ready`/`Valid` conditions are the authoritative
  check — there is no local validator in this repo).
- No Go code changes → no unit/e2e additions. The live sysdemo walkthrough in §5 is the
  acceptance test; its outcome (including the §3.3 edit-path result) is recorded in the
  deploy notes.

## 8. Out of scope

Marketplace tile (`ProviderMetadata`), OpenFGA/authorization-model changes, portal or
web-component code, denial-message surfacing in the UI (the raw admission `Forbidden`
text is what clients see today), Flux codification of the sysdemo content (the repo
kustomization is the source; wiring it into the platform's GitOps is a platform-repo
concern).
