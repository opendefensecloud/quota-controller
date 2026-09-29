# Quota Platform-Mesh UI Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make quota state visible and actionable in the Platform Mesh portal: consumers see "My quotas" (QuotaClaims), providers see "Quota policies" (ConsumptionQuotas) and "Quota approvals" (QuotaGrants).

**Architecture:** Two declarative `ContentConfiguration` resources (spec §2–3, modeled on the live `s3-buckets` exemplar) hosted in `root:cloud-api-system`, routed to workspaces by the `ui.platform-mesh.io/content-for` label matching their bound quota APIExports. No Go/portal code — manifests + docs only, then live wiring on `sysdemo-local` with an empirical check of the generic detail view's edit capability (spec §3.3).

**Tech Stack:** kcp APIBindings, `ui.platform-mesh.io/v1alpha1 ContentConfiguration` (Luigi config fragments, platform-mesh generic web components), kustomize, the sysdemo-local kind cluster.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-09-quota-platform-mesh-ui-design.md` — §3.0 naming is BINDING: consumer node label "My quotas" (`pathSegment: my-quotas`), provider nodes "Quota policies" (`quota-policies`) and "Quota approvals" (`quota-approvals`), all in one category id `quota`, label "Quota".
- **No `createView` on `quota-claims`** — consumers cannot create claims (maximalPermissionPolicy); the UI must not offer what the API denies.
- Labels on both ContentConfigurations: `ui.platform-mesh.io/content-for: quota-consumer` (claims) / `quota-provider` (grants+policies), plus `ui.platform-mesh.io/entity: core_platform-mesh_io_account`.
- Resource identity uses the UNDERSCORED group `quota_opendefense_cloud`, version `v1alpha1`, `scope: "Cluster"`, `namespace: null`.
- **The live exemplar is ground truth for JSON structure**: `kubectl get contentconfiguration s3-buckets -o yaml` in `root:providers:s3` (the exploration excerpt elided some per-field attributes). If the plan's JSON diverges structurally from the exemplar, the exemplar wins — adapt and note it.
- Commit policy: BATCHED — never run `git commit`; stage and append `gitcs '<message>'` lines to `.superpowers/sdd/1b-commits.txt`.
- Live access: kcp-admin kubeconfig recipe is in `.superpowers/sdd/1b-sysdemo-deploy-report.md` ("Eyeball guide"); all kcp calls use `--server=https://kcp.sysdemo.api.opendefense.cloud:443/clusters/<path>`.
- Read-only toward everything not named in a step; live mutations only in the workspaces/steps listed in Task 2.

---

### Task 1: `config/platform-mesh/` manifests + docs

**Files:**
- Create: `config/platform-mesh/README.md`
- Create: `config/platform-mesh/apibinding-core-platform-mesh.yaml`
- Create: `config/platform-mesh/contentconfiguration-quota-claims.yaml`
- Create: `config/platform-mesh/contentconfiguration-quota-grants.yaml`
- Create: `config/platform-mesh/kustomization.yaml`
- Modify: `docs/getting-started.md` (new "Portal visibility (Platform Mesh)" section)
- Modify: `docs/improvement-backlog.md` (item 8 rewrite)

**Interfaces:**
- Consumes: QuotaClaim/QuotaGrant/ConsumptionQuota field names from `api/v1alpha1/` (verify each `property:` path against the types before finalizing).
- Produces: the `config/platform-mesh/` kustomization Task 2 applies verbatim to `root:cloud-api-system`.

- [ ] **Step 1: Write `config/platform-mesh/README.md`**

```markdown
# Platform Mesh UI add-on

Optional. Makes quota state visible in the Platform Mesh portal: consumers get
"My quotas" (QuotaClaims), providers get "Quota policies" (ConsumptionQuotas)
and "Quota approvals" (QuotaGrants). Apply this kustomization in the quota-ctrl
workspace (`root:cloud-api-system`); it binds `core.platform-mesh.io` and hosts
the two ContentConfigurations routed to workspaces via their bound quota
APIExports (`ui.platform-mesh.io/content-for`). The quota system itself works
without this directory.
```

- [ ] **Step 2: Write `config/platform-mesh/apibinding-core-platform-mesh.yaml`**

```yaml
# Binds the platform-mesh core export so this workspace can host
# ContentConfiguration objects (the type is exported from
# root:platform-mesh-system). Content hosted here is routed to other
# workspaces by the ui.platform-mesh.io/content-for label, matched against
# their bound APIExports — the same convention the s3 provider uses.
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: core.platform-mesh.io
spec:
  reference:
    export:
      name: core.platform-mesh.io
      path: root:platform-mesh-system
```

- [ ] **Step 3: Write `config/platform-mesh/contentconfiguration-quota-claims.yaml`**

```yaml
# Consumer surface (spec §3.1): "My quotas" — the limits that apply to THIS
# workspace and the requests it makes. Deliberately NO createView: consumers
# cannot create claims (maximalPermissionPolicy); the controller pre-creates
# them (ADR-005). Label routes this to every workspace bound to quota-consumer.
apiVersion: ui.platform-mesh.io/v1alpha1
kind: ContentConfiguration
metadata:
  name: quota-claims
  labels:
    ui.platform-mesh.io/content-for: quota-consumer
    ui.platform-mesh.io/entity: core_platform-mesh_io_account
spec:
  inlineConfiguration:
    contentType: json
    content: |
      {
        "name": "quota-claims",
        "luigiConfigFragment": {
          "data": {
            "nodes": [
              {
                "entityType": "main.core_platform-mesh_io_account.namespace",
                "pathSegment": "my-quotas",
                "label": "My quotas",
                "icon": "performance",
                "category": { "id": "quota", "label": "Quota", "isGroup": true, "collapsible": true, "order": 650 },
                "order": 10,
                "keepSelectedForChildren": true,
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-list-view",
                "webcomponent": { "selfRegistered": true },
                "context": {
                  "resourceDefinition": {
                    "apiGroup": "quota_opendefense_cloud",
                    "version": "v1alpha1",
                    "entity": "QuotaClaim",
                    "entityCollection": "QuotaClaims",
                    "scope": "Cluster",
                    "namespace": null,
                    "ui": {
                      "listView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Resource", "property": "spec.governed.resource" },
                          { "label": "Phase", "property": "status.phase" },
                          { "label": "Effective limit", "property": "status.effectiveLimit" },
                          { "label": "Requested", "property": "spec.requestedLimit" },
                          { "label": "Reason", "property": "status.reason" }
                        ]
                      },
                      "detailView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Resource", "property": "spec.governed.resource" },
                          { "label": "API group", "property": "spec.governed.group" },
                          { "label": "Phase", "property": "status.phase" },
                          { "label": "Effective limit", "property": "status.effectiveLimit" },
                          { "label": "Granted", "property": "status.grantedLimit" },
                          { "label": "Requested", "property": "spec.requestedLimit" },
                          { "label": "Reason", "property": "status.reason" },
                          { "label": "Last transition", "property": "status.lastTransitionTime" }
                        ]
                      }
                    }
                  }
                },
                "children": [
                  {
                    "pathSegment": ":claimId",
                    "hideFromNav": true,
                    "defineEntity": {
                      "id": "quota_opendefense_cloud_quotaclaim",
                      "contextKey": "claimId",
                      "graphqlEntity": {
                        "group": "quota_opendefense_cloud",
                        "version": "v1alpha1",
                        "kind": "QuotaClaim",
                        "query": "{ metadata { name } }"
                      }
                    },
                    "context": { "accountId": ":accountId", "resourceId": ":claimId" }
                  }
                ]
              },
              {
                "entityType": "main.core_platform-mesh_io_account.namespace.quota_opendefense_cloud_quotaclaim",
                "pathSegment": "dashboard",
                "label": "Quota claim",
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-detail-view",
                "webcomponent": { "selfRegistered": true },
                "defineEntity": { "id": "dashboard" },
                "compound": { "children": [] }
              }
            ]
          }
        }
      }
```

- [ ] **Step 4: Write `config/platform-mesh/contentconfiguration-quota-grants.yaml`**

```yaml
# Provider surface (spec §3.2): "Quota policies" (the caps I define for my
# service) and "Quota approvals" (requests from MY consumers awaiting or
# holding my decision). Direction-encoding names per spec §3.0 — a workspace
# can be provider AND consumer at once, so bare "Quotas" is ambiguous.
# createView only on grants (providers may create grants proactively).
apiVersion: ui.platform-mesh.io/v1alpha1
kind: ContentConfiguration
metadata:
  name: quota-grants
  labels:
    ui.platform-mesh.io/content-for: quota-provider
    ui.platform-mesh.io/entity: core_platform-mesh_io_account
spec:
  inlineConfiguration:
    contentType: json
    content: |
      {
        "name": "quota-grants",
        "luigiConfigFragment": {
          "data": {
            "nodes": [
              {
                "entityType": "main.core_platform-mesh_io_account.namespace",
                "pathSegment": "quota-policies",
                "label": "Quota policies",
                "icon": "rules",
                "category": { "id": "quota", "label": "Quota", "isGroup": true, "collapsible": true, "order": 650 },
                "order": 20,
                "keepSelectedForChildren": true,
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-list-view",
                "webcomponent": { "selfRegistered": true },
                "context": {
                  "resourceDefinition": {
                    "apiGroup": "quota_opendefense_cloud",
                    "version": "v1alpha1",
                    "entity": "ConsumptionQuota",
                    "entityCollection": "ConsumptionQuotas",
                    "scope": "Cluster",
                    "namespace": null,
                    "ui": {
                      "listView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Resource", "property": "spec.governed.resource" },
                          { "label": "Default limit", "property": "spec.defaultLimit" },
                          { "label": "Auto-approve ceiling", "property": "spec.autoApproveCeiling" }
                        ]
                      },
                      "detailView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Resource", "property": "spec.governed.resource" },
                          { "label": "API group", "property": "spec.governed.group" },
                          { "label": "Mode", "property": "spec.by" },
                          { "label": "Default limit", "property": "spec.defaultLimit" },
                          { "label": "Auto-approve ceiling", "property": "spec.autoApproveCeiling" },
                          { "label": "Identity hash", "property": "status.identityHash" }
                        ]
                      }
                    }
                  }
                },
                "children": [
                  {
                    "pathSegment": ":policyId",
                    "hideFromNav": true,
                    "defineEntity": {
                      "id": "quota_opendefense_cloud_consumptionquota",
                      "contextKey": "policyId",
                      "graphqlEntity": {
                        "group": "quota_opendefense_cloud",
                        "version": "v1alpha1",
                        "kind": "ConsumptionQuota",
                        "query": "{ metadata { name } }"
                      }
                    },
                    "context": { "accountId": ":accountId", "resourceId": ":policyId" }
                  }
                ]
              },
              {
                "entityType": "main.core_platform-mesh_io_account.namespace.quota_opendefense_cloud_consumptionquota",
                "pathSegment": "dashboard",
                "label": "Quota policy",
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-detail-view",
                "webcomponent": { "selfRegistered": true },
                "defineEntity": { "id": "dashboard" },
                "compound": { "children": [] }
              },
              {
                "entityType": "main.core_platform-mesh_io_account.namespace",
                "pathSegment": "quota-approvals",
                "label": "Quota approvals",
                "icon": "approvals",
                "category": { "id": "quota", "label": "Quota", "isGroup": true, "collapsible": true, "order": 650 },
                "order": 30,
                "keepSelectedForChildren": true,
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-list-view",
                "webcomponent": { "selfRegistered": true },
                "context": {
                  "resourceDefinition": {
                    "apiGroup": "quota_opendefense_cloud",
                    "version": "v1alpha1",
                    "entity": "QuotaGrant",
                    "entityCollection": "QuotaGrants",
                    "scope": "Cluster",
                    "namespace": null,
                    "ui": {
                      "listView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Consumer", "property": "spec.consumer" },
                          { "label": "Requested", "property": "spec.requestedLimit" },
                          { "label": "Granted", "property": "spec.grantedLimit" },
                          { "label": "Decision", "property": "spec.decision" },
                          { "label": "Reason", "property": "spec.reason" }
                        ]
                      },
                      "detailView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name" },
                          { "label": "Consumer", "property": "spec.consumer" },
                          { "label": "Policy", "property": "spec.governedRef" },
                          { "label": "Resource", "property": "spec.governed.resource" },
                          { "label": "API group", "property": "spec.governed.group" },
                          { "label": "Requested", "property": "spec.requestedLimit" },
                          { "label": "Granted", "property": "spec.grantedLimit" },
                          { "label": "Decision", "property": "spec.decision" },
                          { "label": "Reason", "property": "spec.reason" },
                          { "label": "Applied at", "property": "status.appliedAt" },
                          { "label": "Phase", "property": "status.phase" }
                        ]
                      },
                      "createView": {
                        "fields": [
                          { "label": "Name", "property": "metadata.name", "required": true },
                          { "label": "Consumer (logical cluster)", "property": "spec.consumer", "required": true },
                          { "label": "Policy (ConsumptionQuota name)", "property": "spec.governedRef", "required": true },
                          { "label": "Resource", "property": "spec.governed.resource", "required": true },
                          { "label": "API group", "property": "spec.governed.group", "required": true },
                          { "label": "Identity hash", "property": "spec.governed.identityHash", "required": true },
                          { "label": "Granted limit", "property": "spec.grantedLimit" },
                          { "label": "Decision", "property": "spec.decision", "required": true, "placeholder": "Pending" }
                        ]
                      }
                    }
                  }
                },
                "children": [
                  {
                    "pathSegment": ":grantId",
                    "hideFromNav": true,
                    "defineEntity": {
                      "id": "quota_opendefense_cloud_quotagrant",
                      "contextKey": "grantId",
                      "graphqlEntity": {
                        "group": "quota_opendefense_cloud",
                        "version": "v1alpha1",
                        "kind": "QuotaGrant",
                        "query": "{ metadata { name } }"
                      }
                    },
                    "context": { "accountId": ":accountId", "resourceId": ":grantId" }
                  }
                ]
              },
              {
                "entityType": "main.core_platform-mesh_io_account.namespace.quota_opendefense_cloud_quotagrant",
                "pathSegment": "dashboard",
                "label": "Quota grant",
                "url": "/assets/platform-mesh-portal-ui-wc.js#generic-detail-view",
                "webcomponent": { "selfRegistered": true },
                "defineEntity": { "id": "dashboard" },
                "compound": { "children": [] }
              }
            ]
          }
        }
      }
```

- [ ] **Step 5: Write `config/platform-mesh/kustomization.yaml`**

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

# Optional Platform Mesh UI add-on for the quota system. Apply in the
# quota-ctrl workspace (root:cloud-api-system). See README.md.
resources:
  - apibinding-core-platform-mesh.yaml
  - contentconfiguration-quota-claims.yaml
  - contentconfiguration-quota-grants.yaml
```

- [ ] **Step 6: Validate rendering and JSON**

Run: `kubectl kustomize config/platform-mesh/ > /dev/null && echo OK`
Expected: `OK`
Run (JSON well-formedness of both embedded fragments):
`python3 -c "import yaml,json;[json.loads(d['spec']['inlineConfiguration']['content']) for f in ['config/platform-mesh/contentconfiguration-quota-claims.yaml','config/platform-mesh/contentconfiguration-quota-grants.yaml'] for d in yaml.safe_load_all(open(f)) if d]" && echo JSON_OK`
Expected: `JSON_OK`
Also verify every `property:` path in both files against `api/v1alpha1/quotaclaim_types.go`, `quotagrant_types.go`, `consumptionquota_types.go` (JSON tags) — fix any mismatch.

- [ ] **Step 7: Add "Portal visibility (Platform Mesh)" to `docs/getting-started.md`**

Append after the provider section:

```markdown
## Portal visibility (Platform Mesh)

On Platform Mesh installations, apply the optional UI add-on in the quota-ctrl
workspace (`root:cloud-api-system`):

    kubectl apply -k config/platform-mesh/

Workspaces then see quota state in the portal navigation under a "Quota"
category, with direction-encoding names (a workspace can be provider and
consumer at once):

- **My quotas** (consumer, needs the `quota-consumer` binding): your claims —
  effective limit, request phase, reason.
- **Quota policies** (provider, needs the `quota-provider` binding): the
  ConsumptionQuotas you define.
- **Quota approvals** (provider): your consumers' QuotaGrants — approve or
  reject pending requests.
```

If Task 2's §3.3 edit-path check concludes read-only, Task 2 appends the two
`kubectl patch` fallback commands here — leave that to Task 2.

- [ ] **Step 8: Rewrite `docs/improvement-backlog.md` item 8**

Replace the item 8 block ("Path-aware workspace resolver...") with:

```markdown
8. **~~Path-aware workspace resolver~~ (resolved: not applicable).** The
   "provider workspaces must be direct children of `root`" limitation belongs
   to dependency-controller's resolver and was inherited into this backlog
   speculatively. This repo has no path-resolution code: consumer identifiers
   are opaque logical-cluster strings (hashed in `selfservice.GrantName`, with
   an explicit nested-path test), and APIExport lookups go through
   endpoint-slice URLs. Verified live: `root:providers:s3` (two levels deep)
   runs the full quota stack. The item remains relevant to
   dependency-controller only.
```

- [ ] **Step 9: Stage**

```bash
git add config/platform-mesh/ docs/getting-started.md docs/improvement-backlog.md
```
Append to `.superpowers/sdd/1b-commits.txt`:
`gitcs 'feat(platform-mesh): ContentConfigurations for quota visibility in the portal'`

---

### Task 2: Live wiring on sysdemo + empirical edit-path check + eyeball guide

**Files:**
- Create: `.superpowers/sdd/ui-sysdemo-notes.md` (deploy notes + eyeball guide; gitignored scratch)
- Possibly modify: `config/platform-mesh/contentconfiguration-*.yaml` (ONLY if the live extension-manager validator or exemplar comparison forces structural JSON changes — re-stage and note every change)
- Possibly modify: `docs/getting-started.md` (§3.3 read-only fallback commands, per Task 1 Step 7's note)

**Interfaces:**
- Consumes: Task 1's `config/platform-mesh/` kustomization; the kcp-admin kubeconfig recipe from `.superpowers/sdd/1b-sysdemo-deploy-report.md`.
- Produces: live portal visibility + the recorded §3.3 verdict.

- [ ] **Step 1: Build the kcp-admin kubeconfig** (recipe from the 1b deploy report's Eyeball guide; write to `/tmp/ui-kcp-admin.kubeconfig`, `export KUBECONFIG=`).

- [ ] **Step 2: Compare JSON structure against the live exemplar**

Run: `kubectl --server=.../clusters/root:providers:s3 get contentconfiguration s3-buckets -o yaml`
Compare per-field attribute shape (the plan's field entries carry only `label`/`property`/`required`/`placeholder`; the exemplar may carry more). Adapt Task 1's two files to match the exemplar's structure exactly where they differ; re-run Task 1 Step 6 validation after any change.

- [ ] **Step 3: Apply the add-on**

Run: `kubectl --server=.../clusters/root:cloud-api-system apply -k config/platform-mesh/`
Expected: APIBinding + 2 ContentConfigurations created.
Wait/verify: APIBinding phase `Bound`; then both CCs reach conditions `Ready=True`, `Valid=True`:
`kubectl --server=.../clusters/root:cloud-api-system get contentconfiguration quota-claims quota-grants -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.status.conditions[?(@.type=="Valid")].status}{"\n"}{end}'`
If `Valid=False`: read `.status.conditions[].message`, fix the JSON, re-apply, repeat. Record every fix.

- [ ] **Step 4: Bind quota-consumer in the three acme accounts**

For each of `root:orgs:acme:demo`, `root:orgs:acme:quota-test`, `root:orgs:acme:tui-test-2`:

```yaml
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: quota-consumer
spec:
  reference:
    export:
      name: quota-consumer
      path: root:cloud-api-system
```

Then verify claims appear (within ~1 resync, 60s): `kubectl --server=.../clusters/root:orgs:acme:<ws> get quotaclaims` shows `qc-buckets-<id8>` with `status.effectiveLimit: 4`.

- [ ] **Step 5: Delete the fixture workspaces**

Run: `kubectl --server=.../clusters/root delete workspace quota-consumer1 quota-s3-provider`
(consumer first — its unbind lets discovery GC the claim). Verify the controller logs show clean claim GC / no crash-looping afterwards:
`kubectl --context kind-sysdemo-local -n quota-system logs deploy/<controller> --since=5m | grep -iE "error|panic" | head` (adjust names to the live release).

- [ ] **Step 6: Portal verification + §3.3 edit-path check**

The portal is browser-driven (Keycloak login) — use the Playwright browser tools (mcp browser_navigate etc.) against `https://acme.sysdemo.api.opendefense.cloud`. If interactive login as `cyrill.berg@opendefense.cloud` is not possible for the agent (credentials are the user's), verify what is verifiable headlessly (portal serves 200, extension-manager conditions green, graphql-gateway lists quotaclaims for the org) and hand the interactive pass to the user via the eyeball guide — state exactly which checks were and were not performed.
Empirical §3.3 check: determine whether `generic-detail-view` supports field editing (inspect the web component's exposed config in `/assets/platform-mesh-portal-ui-wc.js`, the exemplar's usage, or the portal validator's schema — e.g. grep the asset for `editView`/`editable`/`patch`). Record the verdict in the notes file. If read-only: append to `docs/getting-started.md`'s portal section:

```markdown
The generic views are read-only for these actions today; use the CLI:

    # consumer: request a raise
    kubectl patch quotaclaim <name> --type=merge -p '{"spec":{"requestedLimit":8}}'
    # provider: approve
    kubectl patch quotagrant <name> --type=merge -p '{"spec":{"decision":"Approved","grantedLimit":8}}'
```

(and re-stage `docs/getting-started.md`).

- [ ] **Step 7: Write `.superpowers/sdd/ui-sysdemo-notes.md`**

Deploy transcript (every command + output), JSON adaptations made in Step 2/3, the §3.3 verdict with evidence, and an **Eyeball guide**: portal URL, login identity, exactly where to click for "My quotas" in `quota-test` (at 4/4 buckets — request a raise demos immediately), and where the provider sees "Quota approvals" for `root:providers:s3`.

- [ ] **Step 8: Stage any modified files**

```bash
git add config/platform-mesh/ docs/getting-started.md
```
If files changed beyond Task 1's commit, append:
`gitcs 'fix(platform-mesh): adapt ContentConfigurations to live validator'`

---

## Self-review notes

- Spec coverage: §2 wiring (T1 S2, T2 S3-4), §3.0 naming (T1 S3-4 labels/pathSegments), §3.1/3.2 field lists (T1 S3-4), §3.3 empirical gate (T2 S6), §4 placement (T1), §5 live wiring incl. fixture deletion + acceptance (T2), §6 backlog rewrite (T1 S8), §7 testing (T1 S6, T2 S3/S6). No gaps.
- The JSON is best-known-structure; Task 2 Step 2's exemplar comparison is the designed correction point, not a placeholder.
- Type consistency: all `property:` paths match `api/v1alpha1` JSON tags (`governedRef` is a string — flat property, not nested).
