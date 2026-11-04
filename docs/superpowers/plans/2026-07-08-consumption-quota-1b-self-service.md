# Consumption Quota — Iteration 1b (Self-Service) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consumers can see their enforced quota and request raises (`QuotaClaim`); providers approve in their own workspace (`QuotaGrant`), with auto-approval at/below `autoApproveCeiling`; the webhook enforces per-consumer granted limits.

**Architecture:** Implements spec §5.3/§6.2–6.3/§8–10 and ADR-002 on top of the deployed 1a enforcement core. Two new cluster-scoped types: `QuotaGrant` (provider workspace, shipped in the existing **quota-provider** APIExport) and `QuotaClaim` (consumer workspace, shipped in a new **quota-consumer** APIExport whose `maximalPermissionPolicy` makes claims consumer-update-only). A request/approval layer inside the existing leader-elected controller binary bridges them; the webhook's in-memory registry gains per-consumer grants for effective-limit resolution (§9). Claim discovery uses the governed service-export VW (spike-verified in Task 1; fallback documented there).

**Tech Stack:** Go 1.26.4, kubebuilder/controller-gen v0.19.0, kcp SDK (`apis.kcp.io/v1alpha1+v1alpha2`), multicluster-runtime + kcp multicluster-provider, Ginkgo/Gomega, envtest 1.31.0, Helm, kind+kcp e2e harness (`test/e2e`).

## Global Constraints

- Go module: `go.opendefense.cloud/quota-controller`; Go `1.26.4` (matches `go.mod` and Dockerfile digest pin).
- Every new `.go` file starts with the two license header lines used repo-wide:
  `// Copyright 2026 BWI GmbH and Quota Controller contributors` / `// SPDX-License-Identifier: Apache-2.0`.
- Tests: Ginkgo v2 + Gomega, table style as in existing suites; run with `-race`.
- Lint: `make lint` must pass (golangci-lint incl. `modernize`: use `new(expr)` for pointer literals, e.g. `new(true)`, NOT `ptr.To`).
- Full verify command: `make test` (uses setup-envtest 1.31.0). Focused: `KUBEBUILDER_ASSETS="$(bin/go/setup-envtest use -p path 1.31.0)" go test -race <pkg>`.
- **Commit policy (repo precedent from 1a): BATCHED.** Implementers NEVER run `git commit` (signing needs the user's hardware token). "Commit" steps below mean: `git add` the listed files and append the suggested `gitcs '<message>'` line to `.superpowers/sdd/1b-commits.txt`. The user runs the gitcs batch at the end.
- Branch: all work happens on `feature/no-ref/consumption-quota-1b`.
- Fail-closed bias everywhere (ADR-003): unknown/unsynced state must never raise a limit.
- Enforcement invariant (ADR-002): the effective limit is computed ONLY from provider-workspace objects (`ConsumptionQuota`, `QuotaGrant`). Nothing read from a consumer workspace may influence it.

---

### Task 1: Spike — verify claim-discovery signal and maximalPermissionPolicy semantics

**Files:**
- Create: `docs/superpowers/specs/2026-07-08-1b-spike-notes.md`
- Create: `architecture/ADR-005-claim-discovery-via-service-export-vw.md`
- Temporary (delete before finishing): `test/e2e/spike_1b_test.go`

**Interfaces:**
- Produces: the verified discovery mechanism (primary hypothesis: consumers' `APIBinding`s are listable/watchable through the governed service APIExport's virtual workspace at binding time, before any governed object exists) and verified MPP verb behavior. Tasks 7–8 consume the decision; if the fallback is needed, Task 7's discovery source changes as documented there.

The two load-bearing, unverified kcp behaviors (spec §14.2, §14.5):

1. **Discovery:** does `{serviceVW}/clusters/*` serve `apibindings.apis.kcp.io` (or otherwise reveal a bound consumer's logical cluster) as soon as a consumer binds — before it creates any governed object?
2. **MPP:** does a `maximalPermissionPolicy` of `update/patch+get/list/watch` on `quotaclaims` and `get/list/watch` on `quotaclaims/status` (no `create`, no `delete`) actually deny a consumer-workspace admin `create`/`delete`/status-writes while allowing spec updates?

- [ ] **Step 1: Bring up the e2e environment** (same harness the 1a suite uses)

Run: `make test-e2e testargs='-ginkgo.focus="provider policy"'` once to confirm the harness works on this machine (kind + kcp + helm must be installed). Expected: the focused 1a spec passes.

- [ ] **Step 2: Write the spike test (throwaway)**

Create `test/e2e/spike_1b_test.go` inside the existing `e2e` package, reusing the suite's helpers for workspace creation, APIExport/APIBinding application, and kubeconfig construction (see `suite_test.go` for the helper names — reuse whatever the 1a suite uses to create the consumer workspace and bind the service export):

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("[spike] 1b discovery + MPP", Ordered, func() {
	It("lists a fresh consumer's APIBinding through the service VW before any object exists", func() {
		// 1. Create a NEW consumer workspace (no governed objects ever created).
		// 2. Bind the provider's service APIExport in it.
		// 3. Build a client against {serviceVW-url}/clusters/* (same construction as
		//    cmd/webhook/registry_watcher.go virtualWorkspaceClients).
		// 4. List apisv1alpha1.APIBindingList through it.
		var list apisv1alpha1.APIBindingList
		Expect(serviceVWClient.List(ctx, &list)).To(Succeed())
		// Expect an item whose kcp.io/cluster annotation == the new consumer's
		// logical cluster and whose spec references the service export.
	})

	It("MPP denies consumer create/delete/status-write on quotaclaims but allows spec update", func() {
		// Apply a THROWAWAY APIExport "spike-consumer" exporting any small CRD
		// (reuse the ConsumptionQuota schema for convenience) with:
		//   spec.maximalPermissionPolicy.local.rules:
		//     - apiGroups: ["quota.opendefense.cloud"]
		//       resources: ["consumptionquotas"]        # stand-in resource
		//       verbs: ["get","list","watch","update","patch"]
		//     - apiGroups: ["quota.opendefense.cloud"]
		//       resources: ["consumptionquotas/status"]
		//       verbs: ["get","list","watch"]
		// Bind it in the consumer workspace; pre-create one instance AS THE EXPORT
		// OWNER via the VW. Then, AS THE CONSUMER-WORKSPACE ADMIN identity:
		//   create  -> expect apierrors.IsForbidden
		//   delete  -> expect apierrors.IsForbidden
		//   status update -> expect apierrors.IsForbidden
		//   spec update   -> expect Succeed
	})
})
```

Fill in the plumbing from the suite helpers; the assertions above are the deliverable. This test is throwaway scaffolding — precision matters in the assertions, not the style.

- [ ] **Step 3: Run the spike**

Run: `make test-e2e testargs='-ginkgo.focus="spike"'`
Expected: both specs pass (primary hypothesis holds), or a precise failure telling us which fallback applies.

- [ ] **Step 4: Record results in `docs/superpowers/specs/2026-07-08-1b-spike-notes.md`**

Document: exact kcp version tested, whether APIBindings are served through the service VW at binding time (Y/N + the actual list output shape), whether MPP behaved per hypothesis (Y/N + exact Forbidden messages), and the decision for Task 7 (primary vs fallback).

- [ ] **Step 5: Write `architecture/ADR-005-claim-discovery-via-service-export-vw.md`**

Follow the ADR-001..004 format (Status/Date/Related/Context/Decision/Alternatives/Consequences). Decision: claim discovery watches consumers' `APIBinding`s through the governed service APIExport's VW (or the spike-selected fallback). Alternatives: APIBinding permissionClaim on the quota-consumer export (rejected: acceptance friction, adversarial consumers can decline), claim-on-first-object (rejected: no visibility before first use), provider-maintained consumer list (rejected: manual drift). Consequences: discovery reuses the per-governed-export accounting VW config; a consumer briefly has no claim between binding and the next discovery sweep (bounded by the resync interval).

- [ ] **Step 6: Delete the spike test and stage**

```bash
rm test/e2e/spike_1b_test.go
git add docs/superpowers/specs/2026-07-08-1b-spike-notes.md architecture/ADR-005-claim-discovery-via-service-export-vw.md
```
Suggested message: `gitcs 'docs: record 1b discovery/MPP spike results and ADR-005'`

---

### Task 2: API types — `QuotaGrant` and `QuotaClaim`

**Files:**
- Create: `api/v1alpha1/quotagrant_types.go`
- Create: `api/v1alpha1/quotaclaim_types.go`
- Modify: `api/v1alpha1/consumptionquota_types.go` (un-reserve the `AutoApproveCeiling` comment)
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crds/quota.opendefense.cloud_quotagrants.yaml`, `config/crds/quota.opendefense.cloud_quotaclaims.yaml`

**Interfaces:**
- Produces (consumed by Tasks 4–9):
  - `type GovernedIdentity struct { Group, Resource, IdentityHash string }`
  - `type GrantDecision string` — `GrantPending|GrantApproved|GrantRejected` = `"Pending"|"Approved"|"Rejected"`
  - `type ClaimPhase string` — `ClaimNone|ClaimPending|ClaimApproved|ClaimRejected` = `"None"|"Pending"|"Approved"|"Rejected"`
  - `QuotaGrant{Spec: QuotaGrantSpec{Consumer string; GovernedRef string; Governed GovernedIdentity; RequestedLimit *int32; GrantedLimit *int32; Decision GrantDecision; Reason string}, Status: QuotaGrantStatus{Phase GrantDecision; AppliedAt *metav1.Time}}`
  - `QuotaClaim{Spec: QuotaClaimSpec{Governed GovernedIdentity; RequestedLimit *int32}, Status: QuotaClaimStatus{Phase ClaimPhase; EffectiveLimit *int32; GrantedLimit *int32; Reason string; LastTransitionTime *metav1.Time}}`

- [ ] **Step 1: Write `api/v1alpha1/quotagrant_types.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GovernedIdentity is the kcp identity tuple addressing one exported resource
// type (spec §6.2/§6.3). Stamped by the controller from the governed
// APIExport's status.identityHash — never trusted from consumer input.
type GovernedIdentity struct {
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
	// +kubebuilder:validation:MinLength=1
	IdentityHash string `json:"identityHash"`
}

// GrantDecision is the provider's (or auto-approval's) verdict on a request.
// +kubebuilder:validation:Enum=Pending;Approved;Rejected
type GrantDecision string

const (
	GrantPending  GrantDecision = "Pending"
	GrantApproved GrantDecision = "Approved"
	GrantRejected GrantDecision = "Rejected"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaGrant is a per-(consumer, governed resource) limit override living in
// the PROVIDER workspace (spec §6.2). The provider approves/rejects here; the
// request/approval controller writes it on auto-approve. Enforcement's only
// override source (§9).
type QuotaGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaGrantSpec   `json:"spec"`
	Status            QuotaGrantStatus `json:"status,omitempty"`
}

type QuotaGrantSpec struct {
	// Consumer is the consumer workspace's logical cluster name.
	// +kubebuilder:validation:MinLength=1
	Consumer string `json:"consumer"`
	// GovernedRef names the ConsumptionQuota this grant overrides (same workspace).
	// +kubebuilder:validation:MinLength=1
	GovernedRef string `json:"governedRef"`
	// Governed is the identity tuple, stamped by the controller.
	Governed GovernedIdentity `json:"governed"`
	// RequestedLimit mirrors the consumer's claim (informational for the provider).
	// +kubebuilder:validation:Minimum=0
	// +optional
	RequestedLimit *int32 `json:"requestedLimit,omitempty"`
	// GrantedLimit is the limit that takes effect when Decision is Approved.
	// +kubebuilder:validation:Minimum=0
	// +optional
	GrantedLimit *int32 `json:"grantedLimit,omitempty"`
	// +kubebuilder:default=Pending
	Decision GrantDecision `json:"decision"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

type QuotaGrantStatus struct {
	// Phase echoes the last decision the controller acted on.
	// +optional
	Phase GrantDecision `json:"phase,omitempty"`
	// AppliedAt is when the controller propagated the decision (registry + claim).
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaGrant `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaGrant{}, &QuotaGrantList{}) }
```

- [ ] **Step 2: Write `api/v1alpha1/quotaclaim_types.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ClaimPhase is the consumer-visible state of a request (spec §6.3).
// +kubebuilder:validation:Enum=None;Pending;Approved;Rejected
type ClaimPhase string

const (
	ClaimNone     ClaimPhase = "None"
	ClaimPending  ClaimPhase = "Pending"
	ClaimApproved ClaimPhase = "Approved"
	ClaimRejected ClaimPhase = "Rejected"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaClaim is the consumer-facing request+view object (spec §6.3). The
// controller pre-creates one per (consumer, governed resource); consumers may
// ONLY update spec.requestedLimit (enforced by the quota-consumer export's
// maximalPermissionPolicy, §10) and read status. Enforcement NEVER reads this
// type (ADR-002) — it only triggers the provider-gated approval path.
type QuotaClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaClaimSpec   `json:"spec"`
	Status            QuotaClaimStatus `json:"status,omitempty"`
}

type QuotaClaimSpec struct {
	// Governed is the identity tuple, stamped by the controller at pre-creation.
	Governed GovernedIdentity `json:"governed"`
	// RequestedLimit is the ONLY field consumers change. Omitted = no request,
	// the claim just shows the current effective limit.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RequestedLimit *int32 `json:"requestedLimit,omitempty"`
}

type QuotaClaimStatus struct {
	// +optional
	Phase ClaimPhase `json:"phase,omitempty"`
	// EffectiveLimit is what the webhook enforces right now (§9).
	// +optional
	EffectiveLimit *int32 `json:"effectiveLimit,omitempty"`
	// GrantedLimit is the approved override, if any.
	// +optional
	GrantedLimit *int32 `json:"grantedLimit,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaClaim `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaClaim{}, &QuotaClaimList{}) }
```

- [ ] **Step 3: Un-reserve the ceiling comment in `api/v1alpha1/consumptionquota_types.go`**

Replace:
```go
	// AutoApproveCeiling is reserved for Phase 2 (self-service) and ignored in Phase 1.
```
with:
```go
	// AutoApproveCeiling: QuotaClaim requests at/below this value are granted
	// automatically; above it (or when omitted) they become Pending for manual
	// provider approval (spec §8).
```

- [ ] **Step 4: Generate deepcopy + CRDs and verify**

Run: `make generate manifests && go build ./...`
Expected: `zz_generated.deepcopy.go` gains the new types; `config/crds/quota.opendefense.cloud_quotagrants.yaml` and `..._quotaclaims.yaml` appear; build passes.

- [ ] **Step 5: Stage**

```bash
git add api/v1alpha1/ config/crds/
```
Suggested message: `gitcs 'feat(api): add QuotaGrant and QuotaClaim types for self-service'`

---

### Task 3: kcp manifests — quota-consumer APIExport with maximalPermissionPolicy; grants in quota-provider

**Files:**
- Create: `config/kcp/apiexport-quota-consumer.yaml`
- Create: `config/kcp/apiresourceschema-quotagrants.quota.opendefense.cloud.yaml`
- Create: `config/kcp/apiresourceschema-quotaclaims.quota.opendefense.cloud.yaml`
- Modify: `config/kcp/apiexport-quota-provider.yaml` (add quotagrants resource)
- Modify: `config/kcp/kustomization.yaml`

**Interfaces:**
- Produces: export name `quota-consumer` (flag default in Task 8), schema names with prefix `v260708-<hash>` (generated), MPP rules per §10. Task 9 applies these in e2e; the chart ships them like the 1a files.

- [ ] **Step 1: Generate APIResourceSchemas from the new CRDs**

Use the same flow that produced the 1a schemas (kcp `crd snapshot`; the kubectl-kcp plugin ships it):

```bash
kubectl kcp crd snapshot -f config/crds/quota.opendefense.cloud_quotagrants.yaml --prefix v260708 \
  > config/kcp/apiresourceschema-quotagrants.quota.opendefense.cloud.yaml
kubectl kcp crd snapshot -f config/crds/quota.opendefense.cloud_quotaclaims.yaml --prefix v260708 \
  > config/kcp/apiresourceschema-quotaclaims.quota.opendefense.cloud.yaml
```

Note the exact generated names (`v260708-<hash>.quotagrants...` / `...quotaclaims...`) — they are referenced verbatim below. Add a provenance comment at the top of each file matching the 1a schema files.

- [ ] **Step 2: Add `quotagrants` to `config/kcp/apiexport-quota-provider.yaml`**

Append under `resources:` (keeping the existing consumptionquotas entry and comments):

```yaml
  # QuotaGrant carries per-consumer decisions/overrides. Providers approve HERE,
  # in their own workspace; the webhook watches grants via this export's VW for
  # effective-limit resolution (spec §9).
  - group: quota.opendefense.cloud
    name: quotagrants
    schema: v260708-<hash>.quotagrants.quota.opendefense.cloud   # exact name from Step 1
    storage:
      crd: {}
```

- [ ] **Step 3: Create `config/kcp/apiexport-quota-consumer.yaml`**

```yaml
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: quota-consumer
spec:
  # Consumers bind this export to see and request quota (QuotaClaim).
  #
  # The maximalPermissionPolicy is the R10 workflow-integrity guard (spec §10,
  # ADR-002): bound identities may UPDATE spec.requestedLimit on claims the
  # controller pre-created and READ status — but cannot create or delete
  # claims, and cannot write status. The controller writes via this export's
  # VW and is not subject to the policy.
  maximalPermissionPolicy:
    local:
      rules:
        - apiGroups: ["quota.opendefense.cloud"]
          resources: ["quotaclaims"]
          verbs: ["get", "list", "watch", "update", "patch"]
        - apiGroups: ["quota.opendefense.cloud"]
          resources: ["quotaclaims/status"]
          verbs: ["get", "list", "watch"]
  resources:
    - group: quota.opendefense.cloud
      name: quotaclaims
      schema: v260708-<hash>.quotaclaims.quota.opendefense.cloud   # exact name from Step 1
      storage:
        crd: {}
status: {}
```

If the Task 1 spike found a different MPP field shape on this kcp version, use the spike-verified YAML — the verbs matrix is the invariant, not the field path.

- [ ] **Step 4: Register the new files in `config/kcp/kustomization.yaml`**

Add to `resources:` (after the existing schema entries):

```yaml
  - apiresourceschema-quotagrants.quota.opendefense.cloud.yaml
  - apiresourceschema-quotaclaims.quota.opendefense.cloud.yaml
  - apiexport-quota-consumer.yaml
```

- [ ] **Step 5: Validate rendering and stage**

Run: `kubectl kustomize config/kcp/ > /dev/null && echo OK` — Expected: `OK`.

```bash
git add config/kcp/
```
Suggested message: `gitcs 'feat(kcp): quota-consumer export with maximalPermissionPolicy; export quotagrants'`

---

### Task 4: Registry — per-consumer grants (effective-limit resolution, §9)

**Files:**
- Modify: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go` (extend)

**Interfaces:**
- Consumes: existing `ResourceRef`, `Registry.Set/Delete/LimitFor/ByGroupResource`.
- Produces (consumed by Tasks 5, 8):
  - `func (r *Registry) SetGrant(cluster string, ref ResourceRef, limit int32)`
  - `func (r *Registry) DeleteGrant(cluster string, ref ResourceRef)`
  - `LimitFor(cluster string, ref ResourceRef) (int32, bool)` now returns the grant when one exists, else the default. A grant for a ref with NO registered default still resolves (grant wins; policy may sync later) — but `LimitFor` returns `false` when neither exists (fail-closed at the caller, ADR-003).

- [ ] **Step 1: Write the failing tests** (append to `internal/registry/registry_test.go`, matching its existing style)

```go
var _ = Describe("per-consumer grants", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("prefers an approved grant over the default limit", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(8)))
	})

	It("falls back to the default for consumers without a grant", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:other", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(3)))
	})

	It("reverts to the default when a grant is deleted", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)
		r.DeleteGrant("root:c1", ref)

		l, _ := r.LimitFor("root:c1", ref)
		Expect(l).To(Equal(int32(3)))
	})

	It("resolves a grant even when no default is registered yet", func() {
		r := registry.New()
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(8)))
	})

	It("returns not-ok when neither grant nor default exists (fail-closed)", func() {
		r := registry.New()

		_, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeFalse())
	})
})
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -race ./internal/registry/`
Expected: FAIL — `r.SetGrant undefined`.

- [ ] **Step 3: Implement**

In `internal/registry/registry.go`: add the grant index and replace `LimitFor`:

```go
// grantKey addresses one consumer's override for one governed resource.
type grantKey struct {
	Cluster string
	Ref     ResourceRef
}
```

Add `grants map[grantKey]int32` to the `Registry` struct, initialize it in `New()` (`grants: map[grantKey]int32{}`), and:

```go
// SetGrant records an APPROVED per-consumer override (spec §9). Pending and
// Rejected grants must never be Set — callers delete instead.
func (r *Registry) SetGrant(cluster string, ref ResourceRef, limit int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grants[grantKey{Cluster: cluster, Ref: ref}] = limit
}

func (r *Registry) DeleteGrant(cluster string, ref ResourceRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.grants, grantKey{Cluster: cluster, Ref: ref})
}

// LimitFor returns the effective limit for a consumer (spec §9): an approved
// grant wins; otherwise the policy default. ok=false means neither exists —
// the webhook fails closed on that (ADR-003).
func (r *Registry) LimitFor(cluster string, ref ResourceRef) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if l, ok := r.grants[grantKey{Cluster: cluster, Ref: ref}]; ok {
		return l, true
	}
	l, ok := r.limits[ref]

	return l, ok
}
```

- [ ] **Step 4: Run tests + webhook package (its handler calls LimitFor)**

Run: `KUBEBUILDER_ASSETS="$(bin/go/setup-envtest use -p path 1.31.0)" go test -race ./internal/registry/ ./internal/webhook/`
Expected: PASS.

- [ ] **Step 5: Stage**

```bash
git add internal/registry/
```
Suggested message: `gitcs 'feat(registry): per-consumer grant overrides in effective-limit resolution'`

---

### Task 5: Webhook — mirror `QuotaGrant`s into the registry

**Files:**
- Create: `cmd/webhook/grant_watcher.go`
- Modify: `cmd/webhook/registry_watcher.go` (PopulateRegistry also seeds grants)
- Modify: `cmd/webhook/main.go` (wire the new controller)

**Interfaces:**
- Consumes: Task 4 `SetGrant/DeleteGrant`; Task 2 `QuotaGrant`; existing quota-provider VW multicluster manager in `cmd/webhook/main.go`.
- Produces: approved grants visible to `CreationValidator` through `Registry.LimitFor` — no webhook-handler changes needed.

The quota-provider VW serves `quotagrants` after Task 3, so the existing manager watches them with a second thin mirror, symmetric to `registryWatcher`.

- [ ] **Step 1: Write `cmd/webhook/grant_watcher.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// grantWatcher mirrors APPROVED QuotaGrants into the limit registry (spec §9).
// Like registryWatcher it is a read-only mirror: decisions are made by the
// controller binary; the webhook only consumes them.
type grantWatcher struct {
	mgr mcmanager.Manager
	reg *registry.Registry

	mu    sync.Mutex
	known map[string]grantEntry // grantRef (cluster/name) -> last mirrored entry
}

type grantEntry struct {
	consumer string
	ref      registry.ResourceRef
}

func (w *grantWatcher) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("grant", req.Name, "cluster", req.ClusterName)

	cl, err := w.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	grantRef := req.ClusterName.String() + "/" + req.Name

	g := &v1alpha1.QuotaGrant{}
	if err := cl.GetClient().Get(ctx, req.NamespacedName, g); err != nil {
		if client.IgnoreNotFound(err) == nil {
			w.forget(grantRef)
			logger.Info("QuotaGrant gone, override removed")

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	if !g.DeletionTimestamp.IsZero() || g.Spec.Decision != v1alpha1.GrantApproved || g.Spec.GrantedLimit == nil {
		// Pending/Rejected/incomplete grants must not raise or hold a limit.
		w.forget(grantRef)

		return ctrl.Result{}, nil
	}

	entry := grantEntry{
		consumer: g.Spec.Consumer,
		ref: registry.ResourceRef{
			Group:        g.Spec.Governed.Group,
			Resource:     g.Spec.Governed.Resource,
			IdentityHash: g.Spec.Governed.IdentityHash,
		},
	}

	w.mu.Lock()
	old, hadOld := w.known[grantRef]
	w.known[grantRef] = entry
	w.mu.Unlock()

	// A grant edit may re-point consumer or identity; drop the stale override.
	if hadOld && old != entry {
		w.reg.DeleteGrant(old.consumer, old.ref)
	}
	w.reg.SetGrant(entry.consumer, entry.ref, *g.Spec.GrantedLimit)

	return ctrl.Result{}, nil
}

func (w *grantWatcher) forget(grantRef string) {
	w.mu.Lock()
	entry, ok := w.known[grantRef]
	delete(w.known, grantRef)
	w.mu.Unlock()

	if ok {
		w.reg.DeleteGrant(entry.consumer, entry.ref)
	}
}
```

- [ ] **Step 2: Seed grants in `PopulateRegistry`** (`cmd/webhook/registry_watcher.go`)

Inside the per-`vwClient` loop (after the ConsumptionQuota list/remember block), add:

```go
			var grants v1alpha1.QuotaGrantList
			if err := vwClient.List(ctx, &grants); err != nil {
				lastErr = err
				logger.Info("initial QuotaGrant list not ready yet; retrying", "error", err.Error())

				return false, nil
			}

			for i := range grants.Items {
				g := &grants.Items[i]
				if g.Spec.Decision != v1alpha1.GrantApproved || g.Spec.GrantedLimit == nil || !g.DeletionTimestamp.IsZero() {
					continue
				}
				w.reg.SetGrant(g.Spec.Consumer, registry.ResourceRef{
					Group:        g.Spec.Governed.Group,
					Resource:     g.Spec.Governed.Resource,
					IdentityHash: g.Spec.Governed.IdentityHash,
				}, *g.Spec.GrantedLimit)
			}
```

(The seed path bypasses `grantWatcher.known`; the watcher rebuilds `known` from events after start, and its `forget` tolerates unknown refs.)

- [ ] **Step 3: Wire the controller in `cmd/webhook/main.go`** (after the registry-watcher builder block)

```go
	grants := &grantWatcher{mgr: mgr, reg: reg, known: map[string]grantEntry{}}
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("grant-watcher").
		For(&v1alpha1.QuotaGrant{}).
		Complete(mcreconcile.Func(grants.Reconcile)); err != nil {
		setupLog.Error(err, "unable to create grant watcher")
		os.Exit(1)
	}
```

- [ ] **Step 4: Build + full webhook-side test run**

Run: `go build ./... && KUBEBUILDER_ASSETS="$(bin/go/setup-envtest use -p path 1.31.0)" go test -race ./cmd/... ./internal/...`
Expected: PASS (grant mirroring itself is pinned end-to-end in Task 9's e2e; the mapping logic lives in Task 4's tested registry).

- [ ] **Step 5: Stage**

```bash
git add cmd/webhook/
```
Suggested message: `gitcs 'feat(webhook): mirror approved QuotaGrants into the limit registry'`

---

### Task 6: Decision + policy-index primitives (`internal/selfservice`)

**Files:**
- Create: `internal/selfservice/decide.go`
- Create: `internal/selfservice/policyindex.go`
- Create: `internal/selfservice/suite_test.go`
- Test: `internal/selfservice/decide_test.go`, `internal/selfservice/policyindex_test.go`

**Interfaces:**
- Consumes: Task 2 types.
- Produces (consumed by Task 7/8):
  - `func Decide(requested int32, ceiling *int32) v1alpha1.GrantDecision`
  - `type Policy struct { ProviderCluster, CQName string; DefaultLimit int32; AutoApproveCeiling *int32 }`
  - `type PolicyIndex struct{ ... }` with `New() *PolicyIndex`, `Set(ref registry.ResourceRef, p Policy)`, `Delete(ref registry.ResourceRef)`, `Get(ref registry.ResourceRef) (Policy, bool)`
  - `func ClaimName(ref registry.ResourceRef) string` / `func GrantName(ref registry.ResourceRef, consumer string) string` — deterministic object names.

- [ ] **Step 1: Write `internal/selfservice/suite_test.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSelfService(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SelfService Suite")
}
```

- [ ] **Step 2: Write the failing tests**

`internal/selfservice/decide_test.go`:

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Decide", func() {
	It("auto-approves at the ceiling", func() {
		Expect(selfservice.Decide(8, new(int32(8)))).To(Equal(v1alpha1.GrantApproved))
	})
	It("auto-approves below the ceiling", func() {
		Expect(selfservice.Decide(5, new(int32(8)))).To(Equal(v1alpha1.GrantApproved))
	})
	It("routes above-ceiling requests to manual approval", func() {
		Expect(selfservice.Decide(9, new(int32(8)))).To(Equal(v1alpha1.GrantPending))
	})
	It("routes every request to manual approval when no ceiling is set", func() {
		Expect(selfservice.Decide(1, nil)).To(Equal(v1alpha1.GrantPending))
	})
})
```

`internal/selfservice/policyindex_test.go`:

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PolicyIndex", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("stores and retrieves the owning policy for a governed ref", func() {
		idx := selfservice.NewPolicyIndex()
		idx.Set(ref, selfservice.Policy{ProviderCluster: "root:p1", CQName: "bucket-quota", DefaultLimit: 3})

		p, ok := idx.Get(ref)
		Expect(ok).To(BeTrue())
		Expect(p.ProviderCluster).To(Equal("root:p1"))
		Expect(p.CQName).To(Equal("bucket-quota"))
	})

	It("misses after Delete", func() {
		idx := selfservice.NewPolicyIndex()
		idx.Set(ref, selfservice.Policy{ProviderCluster: "root:p1"})
		idx.Delete(ref)

		_, ok := idx.Get(ref)
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("deterministic names", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "abcdef0123456789"}

	It("derives a stable claim name per governed ref", func() {
		Expect(selfservice.ClaimName(ref)).To(Equal("qc-buckets-abcdef01"))
	})
	It("derives a stable grant name per (ref, consumer)", func() {
		Expect(selfservice.GrantName(ref, "2ho8elmi0zqmgnbi")).To(Equal("qg-buckets-abcdef01-2ho8elmi0zqmgnbi"))
	})
	It("keeps names within DNS limits for long resource names", func() {
		long := registry.ResourceRef{Group: "g", Resource: "validatingadmissionpolicybindings", IdentityHash: "abcdef0123456789"}
		Expect(len(selfservice.ClaimName(long))).To(BeNumerically("<=", 63))
	})
})
```

- [ ] **Step 3: Run to verify failure**

Run: `go test -race ./internal/selfservice/`
Expected: FAIL — package missing / undefined symbols.

- [ ] **Step 4: Implement**

`internal/selfservice/decide.go`:

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package selfservice holds the pure decision and indexing primitives for the
// 1b request/approval workflow (spec §8) — no Kubernetes client in here.
package selfservice

import (
	"fmt"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// Decide implements the §8 auto-approval rule: at/below the provider's
// ceiling → Approved; above it, or when no ceiling is set → Pending (manual).
func Decide(requested int32, ceiling *int32) v1alpha1.GrantDecision {
	if ceiling != nil && requested <= *ceiling {
		return v1alpha1.GrantApproved
	}

	return v1alpha1.GrantPending
}

// ClaimName returns the deterministic per-governed-ref QuotaClaim name in a
// consumer workspace. Resource truncated to 20 and identity to 8 chars keeps
// the name well inside the 63-char DNS label limit while staying unique per
// identity (same scheme as webhookEntryName's budget reasoning).
func ClaimName(ref registry.ResourceRef) string {
	return fmt.Sprintf("qc-%s-%s", trunc(ref.Resource, 20), trunc(ref.IdentityHash, 8))
}

// GrantName returns the deterministic QuotaGrant name in the provider
// workspace for one (governed ref, consumer) pair. Logical cluster names are
// lowercase alphanumeric and short, so the result stays a valid object name.
func GrantName(ref registry.ResourceRef, consumer string) string {
	return fmt.Sprintf("qg-%s-%s-%s", trunc(ref.Resource, 20), trunc(ref.IdentityHash, 8), consumer)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}

	return s
}
```

`internal/selfservice/policyindex.go`:

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice

import (
	"sync"

	"go.opendefense.cloud/quota-controller/internal/registry"
)

// Policy is what the claim reconciler needs to know about the owning
// ConsumptionQuota of a governed ref: where it lives and its limits. Fed by
// the CQ reconcile path (controller binary), consumed on claim changes.
type Policy struct {
	ProviderCluster    string
	CQName             string
	DefaultLimit       int32
	AutoApproveCeiling *int32
}

type PolicyIndex struct {
	mu       sync.RWMutex
	policies map[registry.ResourceRef]Policy
}

func NewPolicyIndex() *PolicyIndex {
	return &PolicyIndex{policies: map[registry.ResourceRef]Policy{}}
}

func (i *PolicyIndex) Set(ref registry.ResourceRef, p Policy) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.policies[ref] = p
}

func (i *PolicyIndex) Delete(ref registry.ResourceRef) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.policies, ref)
}

func (i *PolicyIndex) Get(ref registry.ResourceRef) (Policy, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	p, ok := i.policies[ref]

	return p, ok
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/selfservice/`
Expected: PASS.

- [ ] **Step 6: Stage**

```bash
git add internal/selfservice/
```
Suggested message: `gitcs 'feat(selfservice): decision rule, policy index, deterministic names'`

---

### Task 7: Request/approval reconcilers (claim→grant, grant→claim-status, claim ensure/GC)

**Files:**
- Create: `internal/controller/quotaclaim_reconciler.go`
- Create: `internal/controller/quotagrant_reconciler.go`
- Create: `internal/controller/claim_ensurer.go`
- Test: `internal/controller/selfservice_integration_test.go` (envtest, reuses `internal/controller/suite_test.go` infra)

**Interfaces:**
- Consumes: Task 2 types; Task 6 `Decide`, `PolicyIndex`, `Policy`, `ClaimName`, `GrantName`; Task 4 registry semantics (mirrored by the webhook, not called here).
- Produces (wired by Task 8):
  - `type QuotaClaimReconciler struct { ProviderClientFor func(ctx context.Context, providerCluster string) (client.Client, error); Policies *selfservice.PolicyIndex }` with `Reconcile(ctx, claimCluster string, cl client.Client, req ctrl.Request) (ctrl.Result, error)`
  - `type QuotaGrantReconciler struct { ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error); DefaultLimitFor func(ref registry.ResourceRef) (int32, bool); Now func() time.Time }` with `Reconcile(ctx, providerCl client.Client, req ctrl.Request) (ctrl.Result, error)`
  - `type ClaimEnsurer struct { ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error) }` with `Ensure(ctx, consumerCluster string, ref registry.ResourceRef, defaultLimit int32) error` and `Remove(ctx, consumerCluster string, ref registry.ResourceRef) error`

All three take client *factories* so Task 8 can inject VW-scoped clients and envtest can inject the plain test client. **None of them ever trusts claim content for enforcement** — claims only trigger the provider-gated path (Global Constraints).

- [ ] **Step 1: Write the failing envtest specs** (`internal/controller/selfservice_integration_test.go`, package `controller_test`, reusing `k8sClient`/`ctx` from `suite_test.go`; envtest needs the new CRDs — `suite_test.go` already installs everything under `config/crds/`, verify it picks up the two new files)

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// In envtest there is a single API server, so "provider workspace" and
// "consumer workspace" collapse onto the same client. The factories return
// k8sClient; the workspace routing itself is exercised in the e2e suite.
func sameCluster(_ context.Context, _ string) (client.Client, error) { return k8sClient, nil }

var _ = Describe("self-service reconcilers", func() {
	var (
		ref     registry.ResourceRef
		ensurer *controller.ClaimEnsurer
	)

	BeforeEach(func() {
		ref = registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc123def456"}
		ensurer = &controller.ClaimEnsurer{ConsumerClientFor: sameCluster}
	})

	AfterEach(func() {
		_ = k8sClient.DeleteAllOf(ctx, &v1alpha1.QuotaClaim{})
		_ = k8sClient.DeleteAllOf(ctx, &v1alpha1.QuotaGrant{})
	})

	Describe("ClaimEnsurer", func() {
		It("pre-creates a claim with stamped identity and default effective limit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Spec.Governed.IdentityHash).To(Equal("abc123def456"))
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(3))))
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimNone))
		})

		It("is idempotent and preserves an existing requestedLimit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Spec.RequestedLimit).To(HaveValue(Equal(int32(8))))
		})

		It("Remove deletes the pre-created claim", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			Expect(ensurer.Remove(ctx, "root:c1", ref)).To(Succeed())

			err := k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, &v1alpha1.QuotaClaim{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("QuotaClaimReconciler", func() {
		newReconciler := func(ceiling *int32) *controller.QuotaClaimReconciler {
			idx := selfservice.NewPolicyIndex()
			idx.Set(ref, selfservice.Policy{
				ProviderCluster: "root:p1", CQName: "bucket-quota",
				DefaultLimit: 3, AutoApproveCeiling: ceiling,
			})

			return &controller.QuotaClaimReconciler{ProviderClientFor: sameCluster, Policies: idx}
		}

		reconcileClaim := func(r *controller.QuotaClaimReconciler) {
			_, err := r.Reconcile(ctx, "root:c1", k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.ClaimName(ref)}})
			Expect(err).NotTo(HaveOccurred())
		}

		It("writes an Approved grant for a request at/below the ceiling", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10))))

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(8))))
			Expect(grant.Spec.Consumer).To(Equal("root:c1"))
			Expect(grant.Spec.GovernedRef).To(Equal("bucket-quota"))
		})

		It("writes a Pending grant above the ceiling and never sets grantedLimit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(99))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10))))

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantPending))
			Expect(grant.Spec.GrantedLimit).To(BeNil())
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(99))))
		})

		It("does not downgrade an existing Approved grant on a repeat identical request", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			r := newReconciler(new(int32(10)))
			reconcileClaim(r)
			reconcileClaim(r) // second pass must be a no-op

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
		})

		It("ignores claims whose governed ref has no known policy (fail-closed: no grant)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			empty := &controller.QuotaClaimReconciler{ProviderClientFor: sameCluster, Policies: selfservice.NewPolicyIndex()}
			reconcileClaim(empty)

			err := k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, &v1alpha1.QuotaGrant{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("QuotaGrantReconciler", func() {
		fixedNow := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

		writeGrant := func(decision v1alpha1.GrantDecision, granted *int32) {
			grant := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed: v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(8)), GrantedLimit: granted, Decision: decision,
				},
			}
			Expect(k8sClient.Create(ctx, grant)).To(Succeed())
		}

		reconcileGrant := func() {
			r := &controller.QuotaGrantReconciler{
				ConsumerClientFor: sameCluster,
				DefaultLimitFor:   func(_ registry.ResourceRef) (int32, bool) { return 3, true },
				Now:               func() time.Time { return fixedNow },
			}
			_, err := r.Reconcile(ctx, k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}})
			Expect(err).NotTo(HaveOccurred())
		}

		It("writes Approved decisions back to the claim status", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, new(int32(8)))

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimApproved))
			Expect(claim.Status.GrantedLimit).To(HaveValue(Equal(int32(8))))
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(8))))
			Expect(claim.Status.LastTransitionTime.Time).To(Equal(fixedNow))
		})

		It("writes Rejected decisions with the default as effective limit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantRejected, nil)

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimRejected))
			Expect(claim.Status.GrantedLimit).To(BeNil())
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(3))))
		})

		It("stamps grant status.phase and appliedAt", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, new(int32(8)))

			reconcileGrant()

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Status.Phase).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Status.AppliedAt.Time).To(Equal(fixedNow))
		})
	})
})
```

- [ ] **Step 2: Run to verify failure**

Run: `KUBEBUILDER_ASSETS="$(bin/go/setup-envtest use -p path 1.31.0)" go test -race ./internal/controller/ -run TestController`
Expected: FAIL — `controller.ClaimEnsurer` undefined etc.

- [ ] **Step 3: Implement `internal/controller/claim_ensurer.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// ClaimEnsurer pre-creates (and garbage-collects) the per-consumer QuotaClaim
// so the current limit is always visible (spec §5.3 visibility, R11).
// Consumers cannot create claims themselves (maximalPermissionPolicy, §10),
// so this is the ONLY path by which a claim comes to exist.
type ClaimEnsurer struct {
	// ConsumerClientFor returns a client scoped to the consumer's logical
	// cluster (via the quota-consumer VW in production, plain client in tests).
	ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error)
}

// Ensure creates the claim if absent and keeps status.effectiveLimit at the
// default for claims that carry no decision yet. Never touches an existing
// spec (the consumer's requestedLimit is theirs).
func (e *ClaimEnsurer) Ensure(ctx context.Context, consumerCluster string, ref registry.ResourceRef, defaultLimit int32) error {
	cl, err := e.ConsumerClientFor(ctx, consumerCluster)
	if err != nil {
		return fmt.Errorf("consumer client for %s: %w", consumerCluster, err)
	}

	name := selfservice.ClaimName(ref)
	existing := &v1alpha1.QuotaClaim{}
	err = cl.Get(ctx, types.NamespacedName{Name: name}, existing)

	switch {
	case apierrors.IsNotFound(err):
		claim := &v1alpha1.QuotaClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.QuotaClaimSpec{
				Governed: v1alpha1.GovernedIdentity{
					Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash,
				},
			},
		}
		if err := cl.Create(ctx, claim); err != nil {
			return fmt.Errorf("creating claim %s in %s: %w", name, consumerCluster, err)
		}
		claim.Status = v1alpha1.QuotaClaimStatus{
			Phase:          v1alpha1.ClaimNone,
			EffectiveLimit: &defaultLimit,
		}

		return cl.Status().Update(ctx, claim)

	case err != nil:
		return fmt.Errorf("getting claim %s in %s: %w", name, consumerCluster, err)
	}

	// Existing claim: only refresh the default-derived effective limit while no
	// decision applies (a granted claim's status is owned by the grant path).
	if existing.Status.Phase == "" || existing.Status.Phase == v1alpha1.ClaimNone {
		if existing.Status.EffectiveLimit == nil || *existing.Status.EffectiveLimit != defaultLimit {
			existing.Status.Phase = v1alpha1.ClaimNone
			existing.Status.EffectiveLimit = &defaultLimit

			return cl.Status().Update(ctx, existing)
		}
	}

	return nil
}

// Remove garbage-collects the claim when the (consumer, governed resource)
// pair disappears (unbind). Grants stay untouched — provider-owned records.
func (e *ClaimEnsurer) Remove(ctx context.Context, consumerCluster string, ref registry.ResourceRef) error {
	cl, err := e.ConsumerClientFor(ctx, consumerCluster)
	if err != nil {
		return fmt.Errorf("consumer client for %s: %w", consumerCluster, err)
	}

	claim := &v1alpha1.QuotaClaim{ObjectMeta: metav1.ObjectMeta{Name: selfservice.ClaimName(ref)}}
	if err := cl.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting claim in %s: %w", consumerCluster, err)
	}

	return nil
}
```

- [ ] **Step 4: Implement `internal/controller/quotaclaim_reconciler.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// QuotaClaimReconciler turns consumer requests into provider-workspace grants
// (spec §8 steps 2–3). It reads ONLY the requestedLimit trigger from the claim
// — every authorization input (ceiling, default) comes from the provider-side
// PolicyIndex (ADR-002 invariant).
type QuotaClaimReconciler struct {
	// ProviderClientFor returns a client scoped to the provider's logical
	// cluster (via the quota-provider VW in production).
	ProviderClientFor func(ctx context.Context, providerCluster string) (client.Client, error)
	Policies          *selfservice.PolicyIndex
}

// Reconcile handles one claim event. claimCluster is the consumer's logical
// cluster; cl is a client scoped to it (the multicluster manager provides both).
func (r *QuotaClaimReconciler) Reconcile(ctx context.Context, claimCluster string, cl client.Client, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("claim", req.Name, "consumer", claimCluster)

	claim := &v1alpha1.QuotaClaim{}
	if err := cl.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !claim.DeletionTimestamp.IsZero() || claim.Spec.RequestedLimit == nil {
		return ctrl.Result{}, nil // nothing requested; claim is view-only
	}

	ref := registry.ResourceRef{
		Group:        claim.Spec.Governed.Group,
		Resource:     claim.Spec.Governed.Resource,
		IdentityHash: claim.Spec.Governed.IdentityHash,
	}
	policy, ok := r.Policies.Get(ref)
	if !ok {
		// Unknown governed ref: policy not synced (or claim is stale after an
		// identity rotation). Do NOT write a grant — requeue via next event.
		logger.Info("no policy for claim's governed ref; skipping")

		return ctrl.Result{}, nil
	}

	requested := *claim.Spec.RequestedLimit
	decision := selfservice.Decide(requested, policy.AutoApproveCeiling)

	pcl, err := r.ProviderClientFor(ctx, policy.ProviderCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("provider client for %s: %w", policy.ProviderCluster, err)
	}

	name := selfservice.GrantName(ref, claimCluster)
	existing := &v1alpha1.QuotaGrant{}
	err = pcl.Get(ctx, types.NamespacedName{Name: name}, existing)

	switch {
	case apierrors.IsNotFound(err):
		grant := &v1alpha1.QuotaGrant{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.QuotaGrantSpec{
				Consumer:    claimCluster,
				GovernedRef: policy.CQName,
				Governed: v1alpha1.GovernedIdentity{
					Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash,
				},
				RequestedLimit: &requested,
				Decision:       decision,
				Reason:         reasonFor(decision),
			},
		}
		if decision == v1alpha1.GrantApproved {
			grant.Spec.GrantedLimit = &requested
		}
		logger.Info("writing grant", "decision", decision, "requested", requested)

		return ctrl.Result{}, pcl.Create(ctx, grant)

	case err != nil:
		return ctrl.Result{}, fmt.Errorf("getting grant %s: %w", name, err)
	}

	// Existing grant. Mirror the new requestedLimit; escalate the decision only
	// along the auto-approve path. NEVER overwrite a provider's manual verdict:
	// a Rejected grant stays Rejected until the provider edits it, and an
	// Approved grant is only re-approved automatically if the new request still
	// clears the ceiling.
	updated := existing.DeepCopy()
	updated.Spec.RequestedLimit = &requested
	if decision == v1alpha1.GrantApproved && existing.Spec.Decision != v1alpha1.GrantRejected {
		updated.Spec.Decision = v1alpha1.GrantApproved
		updated.Spec.GrantedLimit = &requested
		updated.Spec.Reason = reasonFor(v1alpha1.GrantApproved)
	}
	if equalGrantSpecs(existing.Spec, updated.Spec) {
		return ctrl.Result{}, nil
	}
	logger.Info("updating grant", "decision", updated.Spec.Decision, "requested", requested)

	return ctrl.Result{}, pcl.Update(ctx, updated)
}

func reasonFor(d v1alpha1.GrantDecision) string {
	if d == v1alpha1.GrantApproved {
		return "auto-approved (requestedLimit <= autoApproveCeiling)"
	}

	return "awaiting provider approval (requestedLimit above autoApproveCeiling or no ceiling set)"
}

func equalGrantSpecs(a, b v1alpha1.QuotaGrantSpec) bool {
	int32PtrEq := func(x, y *int32) bool {
		if x == nil || y == nil {
			return x == y
		}

		return *x == *y
	}

	return a.Consumer == b.Consumer && a.GovernedRef == b.GovernedRef && a.Governed == b.Governed &&
		a.Decision == b.Decision && a.Reason == b.Reason &&
		int32PtrEq(a.RequestedLimit, b.RequestedLimit) && int32PtrEq(a.GrantedLimit, b.GrantedLimit)
}
```

- [ ] **Step 5: Implement `internal/controller/quotagrant_reconciler.go`**

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// QuotaGrantReconciler propagates grant decisions to the consumer-visible
// claim status (spec §8 step 5). Enforcement pickup is the webhook's grant
// watcher — this reconciler only owns visibility and grant status stamping.
type QuotaGrantReconciler struct {
	// ConsumerClientFor returns a client scoped to the consumer's logical
	// cluster (via the quota-consumer VW in production).
	ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error)
	// DefaultLimitFor resolves the policy default for a governed ref (backed by
	// the controller's registry mirror); used when no grant applies.
	DefaultLimitFor func(ref registry.ResourceRef) (int32, bool)
	Now             func() time.Time
}

// Reconcile handles one grant event; providerCl is scoped to the provider's
// logical cluster (supplied by the multicluster manager).
func (r *QuotaGrantReconciler) Reconcile(ctx context.Context, providerCl client.Client, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("grant", req.Name)

	grant := &v1alpha1.QuotaGrant{}
	if err := providerCl.Get(ctx, req.NamespacedName, grant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !grant.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // deletion reverts enforcement via the webhook's watcher; claim refresh happens on the next ensure sweep
	}

	ref := registry.ResourceRef{
		Group:        grant.Spec.Governed.Group,
		Resource:     grant.Spec.Governed.Resource,
		IdentityHash: grant.Spec.Governed.IdentityHash,
	}

	// Compute the consumer-visible view.
	status := v1alpha1.QuotaClaimStatus{
		Reason: grant.Spec.Reason,
	}
	now := metav1.NewTime(r.Now())
	status.LastTransitionTime = &now

	switch grant.Spec.Decision {
	case v1alpha1.GrantApproved:
		status.Phase = v1alpha1.ClaimApproved
		status.GrantedLimit = grant.Spec.GrantedLimit
		status.EffectiveLimit = grant.Spec.GrantedLimit
	case v1alpha1.GrantRejected:
		status.Phase = v1alpha1.ClaimRejected
	default:
		status.Phase = v1alpha1.ClaimPending
	}
	if status.EffectiveLimit == nil {
		if def, ok := r.DefaultLimitFor(ref); ok {
			status.EffectiveLimit = &def
		}
	}

	// Write the claim status in the consumer workspace.
	ccl, err := r.ConsumerClientFor(ctx, grant.Spec.Consumer)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("consumer client for %s: %w", grant.Spec.Consumer, err)
	}
	claim := &v1alpha1.QuotaClaim{}
	if err := ccl.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim); err != nil {
		// Claim not (yet) there — e.g. GC'd after unbind. Nothing to update.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if claim.Status.Phase != status.Phase ||
		!int32PtrEqual(claim.Status.EffectiveLimit, status.EffectiveLimit) ||
		!int32PtrEqual(claim.Status.GrantedLimit, status.GrantedLimit) ||
		claim.Status.Reason != status.Reason {
		claim.Status = status
		if err := ccl.Status().Update(ctx, claim); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating claim status in %s: %w", grant.Spec.Consumer, err)
		}
		logger.Info("claim status written", "consumer", grant.Spec.Consumer, "phase", status.Phase)
	}

	// Stamp the grant's own status so providers see the decision was applied.
	if grant.Status.Phase != grant.Spec.Decision {
		grant.Status.Phase = grant.Spec.Decision
		grant.Status.AppliedAt = &now

		return ctrl.Result{}, providerCl.Status().Update(ctx, grant)
	}

	return ctrl.Result{}, nil
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
```

- [ ] **Step 6: Run the integration specs**

Run: `KUBEBUILDER_ASSETS="$(bin/go/setup-envtest use -p path 1.31.0)" go test -race ./internal/controller/`
Expected: PASS (all new specs + all 1a specs).

- [ ] **Step 7: Stage**

```bash
git add internal/controller/
```
Suggested message: `gitcs 'feat(controller): claim/grant reconcilers and claim ensurer for self-service'`

---

### Task 8: Controller binary wiring — consumer VW manager, discovery, policy index, GC

**Files:**
- Create: `cmd/controller/selfservice.go`
- Modify: `cmd/controller/main.go`
- Modify: `cmd/controller/accounting.go` (discovery hook in the per-export sub-manager)
- Test: `cmd/controller/selfservice_test.go` (pure diffing logic)

**Interfaces:**
- Consumes: Task 7 reconcilers/ensurer, Task 6 index, Task 1 spike result (discovery source), existing `accountingManager` seams (`startFn`, per-export sub-managers), `kcp.EndpointSliceName`/`VirtualWorkspaceURLs`.
- Produces: running self-service loop; flag `--consumer-api-export-name` (default `quota-consumer`).

Wiring shape (all in the leader-elected controller binary):

1. **Consumer VW multicluster manager:** resolve the `quota-consumer` endpoint slice (same pattern as the quota-provider one in `main.go`), build `apiexport.New` provider + `mcmanager`, run `QuotaClaimReconciler` on `For(&v1alpha1.QuotaClaim{})`. The mc request carries `ClusterName` (= consumer cluster) and the manager supplies the cluster client — adapt with a small closure exactly like the CQ reconcile closure in `main.go`.
2. **Grant reconciler on the existing provider manager:** `mcbuilder.ControllerManagedBy(mgr).Named("quotagrant").For(&v1alpha1.QuotaGrant{})` with a closure passing the cluster client.
3. **Client factories:** `ProviderClientFor`/`ConsumerClientFor` build `rest.CopyConfig(<vw-base-cfg>)` with `Host = vwURL + "/clusters/" + cluster` — write one helper `vwScopedClientFactory(baseCfg *rest.Config, vwURL string, scheme *runtime.Scheme) func(ctx, cluster) (client.Client, error)` in `cmd/controller/selfservice.go` and use it for both.
4. **PolicyIndex feeding:** in the CQ reconcile closure in `main.go`, after a successful reconcile with stamped identity, `policies.Set(ref, selfservice.Policy{ProviderCluster: req.ClusterName.String(), CQName: cq.Name, DefaultLimit: cq.Spec.DefaultLimit, AutoApproveCeiling: cq.Spec.AutoApproveCeiling})`; on NotFound/deletion, `policies.Delete(ref)` (resolve `ref` from the CQ status stamped identity; on deletion the identity comes from the accounting `Remove` path which already knows the key).
5. **Discovery + claim ensure/GC (spike-verified source):** each accounting sub-manager (`accountingManager.start`) gains a Runnable that every `resync` interval lists `APIBindingList` through the sub-manager's service-VW config (`svcCfg` with `/clusters/*`), maps bindings of the governed export to consumer logical clusters (`logicalcluster.From(&binding)`), and diffs against the previous round: new consumer → `ensurer.Ensure(consumer, ref, policy.DefaultLimit)`; disappeared consumer → `ensurer.Remove(consumer, ref)`. The diffing is a pure function so it is unit-testable:

```go
// diffConsumers returns which consumers to add and which to remove, given the
// currently-bound set and the set claims were last ensured for.
func diffConsumers(bound, ensured sets.Set[string]) (add, remove []string) {
	return sets.List(bound.Difference(ensured)), sets.List(ensured.Difference(bound))
}
```

- [ ] **Step 1: Write the failing test for the diff** (`cmd/controller/selfservice_test.go`, package `main`, alongside the existing lifecycle specs)

```go
var _ = Describe("diffConsumers", func() {
	It("adds newly bound consumers and removes unbound ones", func() {
		add, remove := diffConsumers(sets.New("c1", "c2"), sets.New("c2", "c3"))
		Expect(add).To(ConsistOf("c1"))
		Expect(remove).To(ConsistOf("c3"))
	})
	It("is empty when sets match", func() {
		add, remove := diffConsumers(sets.New("c1"), sets.New("c1"))
		Expect(add).To(BeEmpty())
		Expect(remove).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -race ./cmd/controller/` — Expected: FAIL — `diffConsumers` undefined.

- [ ] **Step 3: Implement `cmd/controller/selfservice.go`**

Contains: `diffConsumers` (as above), `vwScopedClientFactory`, and `claimDiscoveryRunnable` — the Runnable factory used by `accounting.go`:

```go
// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

func diffConsumers(bound, ensured sets.Set[string]) (add, remove []string) {
	return sets.List(bound.Difference(ensured)), sets.List(ensured.Difference(bound))
}

// vwScopedClientFactory returns per-logical-cluster clients through a virtual
// workspace: {vwURL}/clusters/{cluster}.
func vwScopedClientFactory(baseCfg *rest.Config, vwURL string, scheme *runtime.Scheme) func(context.Context, string) (client.Client, error) {
	return func(_ context.Context, cluster string) (client.Client, error) {
		cfg := rest.CopyConfig(baseCfg)
		cfg.Host = vwURL + "/clusters/" + cluster

		return client.New(cfg, client.Options{Scheme: scheme})
	}
}

// claimDiscovery periodically lists APIBindings through one governed export's
// service VW (ADR-005) and reconciles the pre-created QuotaClaim set: bound
// consumer without a claim -> Ensure; ensured consumer no longer bound ->
// Remove (GC). Runs inside the per-export accounting sub-manager, so its
// lifetime and leader-election gating match the export's accounting.
type claimDiscovery struct {
	svcVWClient   client.Client // list APIBindings across {serviceVW}/clusters/*
	exportName    string        // governed service export the bindings must reference
	ref           registry.ResourceRef
	defaultLimit  func() (int32, bool) // policy default at sweep time (PolicyIndex-backed)
	ensurer       *controller.ClaimEnsurer
	interval      time.Duration
	log           logr.Logger

	ensured sets.Set[string]
}

func (d *claimDiscovery) Start(ctx context.Context) error {
	d.ensured = sets.New[string]()
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		if err := d.sweep(ctx); err != nil {
			d.log.Error(err, "claim discovery sweep failed", "export", d.exportName)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (d *claimDiscovery) sweep(ctx context.Context) error {
	var bindings apisv1alpha1.APIBindingList
	if err := d.svcVWClient.List(ctx, &bindings); err != nil {
		return fmt.Errorf("listing APIBindings via service VW: %w", err)
	}

	bound := sets.New[string]()
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Spec.Reference.Export == nil || b.Spec.Reference.Export.Name != d.exportName {
			continue
		}
		bound.Insert(logicalcluster.From(b).String())
	}

	limit, ok := d.defaultLimit()
	if !ok {
		d.log.Info("no policy default yet; skipping claim sweep", "export", d.exportName)

		return nil
	}

	add, remove := diffConsumers(bound, d.ensured)
	for _, consumer := range add {
		if err := d.ensurer.Ensure(ctx, consumer, d.ref, limit); err != nil {
			return err // sweep retries next tick; ensured-set not updated for this consumer
		}
		d.ensured.Insert(consumer)
	}
	for _, consumer := range remove {
		if err := d.ensurer.Remove(ctx, consumer, d.ref); err != nil {
			return err
		}
		d.ensured.Delete(consumer)
	}

	return nil
}
```

(If the Task 1 spike selected the fallback signal, `sweep` lists that source instead — the diff/ensure skeleton is unchanged. Adjust `b.Spec.Reference.Export` field paths to the SDK version if the compiler disagrees; the semantic is "binding references the governed export".)

- [ ] **Step 4: Hook discovery into `accountingManager.start`** (`cmd/controller/accounting.go`)

Add fields to `accountingManager` (wired from `main.go`): `ensurer *controller.ClaimEnsurer`, `policies *selfservice.PolicyIndex`. In `start()`, after the sweep-ticker Runnable is added, add the discovery Runnable (skip when `a.ensurer == nil`, which keeps 1a-only unit tests valid):

```go
	if a.ensurer != nil {
		svcVWURL, err := kcp.VirtualWorkspaceURL(ctx, directClient, g.apiExportName)
		if err != nil {
			return nil, fmt.Errorf("service export %s VW URL: %w", g.apiExportName, err)
		}
		vwCfg := rest.CopyConfig(a.baseCfg)
		vwCfg.Host = svcVWURL + "/clusters/*"
		svcVWClient, err := client.New(vwCfg, client.Options{Scheme: a.scheme})
		if err != nil {
			return nil, fmt.Errorf("service VW client: %w", err)
		}
		ref := registry.ResourceRef{Group: g.group, Resource: g.resource, IdentityHash: g.identityHash}
		disc := &claimDiscovery{
			svcVWClient: svcVWClient,
			exportName:  g.apiExportName,
			ref:         ref,
			defaultLimit: func() (int32, bool) {
				p, ok := a.policies.Get(ref)

				return p.DefaultLimit, ok
			},
			ensurer:  a.ensurer,
			interval: a.resync,
			log:      a.log.WithName("claim-discovery"),
		}
		if err := subMgr.GetLocalManager().Add(disc); err != nil {
			return nil, fmt.Errorf("adding claim discovery runnable: %w", err)
		}
	}
```

- [ ] **Step 5: Wire everything in `cmd/controller/main.go`**

Additions (mirror the existing quota-provider constructions):

```go
	// flag block:
	var consumerAPIExportName string
	flag.StringVar(&consumerAPIExportName, "consumer-api-export-name", "quota-consumer",
		"Name of the quota-consumer APIExport (QuotaClaims are served through its virtual workspace)")
```

After the provider `mgr` is built:

```go
	consumerSliceCtx, consumerSliceCancel := context.WithTimeout(rootCtx, startupRequestTimeout)
	consumerSliceName, err := kcp.EndpointSliceName(consumerSliceCtx, directClient, consumerAPIExportName)
	consumerSliceCancel()
	if err != nil {
		setupLog.Error(err, "unable to find APIExportEndpointSlice", "apiExport", consumerAPIExportName)
		os.Exit(1)
	}
	consumerProvider, err := apiexport.New(cfg, consumerSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create quota-consumer apiexport provider")
		os.Exit(1)
	}
	consumerMgr, err := mcmanager.New(cfg, consumerProvider, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false, // gated by the parent leader election below
	})
	if err != nil {
		setupLog.Error(err, "unable to create quota-consumer manager")
		os.Exit(1)
	}
```

VW URLs + factories + reconcilers (place after `reg`/`store` construction):

```go
	policies := selfservice.NewPolicyIndex()

	providerVWURL, err := kcp.VirtualWorkspaceURL(context.Background(), directClient, apiExportName)   // bounded: rootCtx+timeout like the slice lookups
	consumerVWURL, err2 := kcp.VirtualWorkspaceURL(context.Background(), directClient, consumerAPIExportName)
	// handle errors like the slice lookups (setupLog.Error + os.Exit(1)); use context.WithTimeout(rootCtx, startupRequestTimeout)

	providerClientFor := vwScopedClientFactory(kcpBaseFromCfg, providerVWURL, scheme) // rest base = BaseConfig(cfg)-derived, same as accounting
	consumerClientFor := vwScopedClientFactory(kcpBaseFromCfg, consumerVWURL, scheme)

	ensurer := &controller.ClaimEnsurer{ConsumerClientFor: consumerClientFor}
	acct.ensurer = ensurer
	acct.policies = policies

	claimReconciler := &controller.QuotaClaimReconciler{ProviderClientFor: providerClientFor, Policies: policies}
	if err := mcbuilder.ControllerManagedBy(consumerMgr).
		Named("quotaclaim").
		For(&v1alpha1.QuotaClaim{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			cl, err := consumerMgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			return claimReconciler.Reconcile(ctx, req.ClusterName.String(), cl.GetClient(), ctrl.Request{NamespacedName: req.NamespacedName})
		})); err != nil {
		setupLog.Error(err, "unable to create QuotaClaim controller")
		os.Exit(1)
	}

	grantReconciler := &controller.QuotaGrantReconciler{
		ConsumerClientFor: consumerClientFor,
		DefaultLimitFor: func(ref registry.ResourceRef) (int32, bool) {
			p, ok := policies.Get(ref)

			return p.DefaultLimit, ok
		},
		Now: time.Now,
	}
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("quotagrant").
		For(&v1alpha1.QuotaGrant{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			cl, err := mgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			return grantReconciler.Reconcile(ctx, cl.GetClient(), ctrl.Request{NamespacedName: req.NamespacedName})
		})); err != nil {
		setupLog.Error(err, "unable to create QuotaGrant controller")
		os.Exit(1)
	}

	// The consumer manager must only run on the leader (single writer for
	// grants/claims). Add it as a Runnable of the leader-elected provider
	// manager's local manager so it starts after leadership is acquired.
	if err := mgr.GetLocalManager().Add(consumerMgr); err != nil {
		setupLog.Error(err, "unable to attach quota-consumer manager")
		os.Exit(1)
	}
```

And in the CQ reconcile closure (after the successful-`Ensure` branch), feed the index:

```go
		ref := registry.ResourceRef{
			Group:        cq.Spec.Governed.Group,
			Resource:     cq.Spec.Governed.Resource,
			IdentityHash: cq.Status.IdentityHash,
		}
		policies.Set(ref, selfservice.Policy{
			ProviderCluster:    req.ClusterName.String(),
			CQName:             cq.Name,
			DefaultLimit:       cq.Spec.DefaultLimit,
			AutoApproveCeiling: cq.Spec.AutoApproveCeiling,
		})
```

(and `policies.Delete` next to the existing `acct.Remove(...)` calls — the deletion path needs the ref; extend `accountingManager.Remove` to return the removed key's ref, or track `cqRef -> ref` in a small map beside `policies` in `main.go` — the map variant is simpler: `cqPolicyRefs map[string]registry.ResourceRef` keyed `cluster/name`, guarded by a mutex, written where `policies.Set` happens.)

- [ ] **Step 6: Build + full test run**

Run: `go build ./... && make test`
Expected: build passes; all packages PASS (the 1a accounting lifecycle specs still pass because `ensurer == nil` in those tests skips discovery).

- [ ] **Step 7: Stage**

```bash
git add cmd/controller/
```
Suggested message: `gitcs 'feat(controller): wire self-service loop — consumer VW manager, discovery, policy index'`

---

### Task 9: Helm chart + RBAC + e2e

**Files:**
- Modify: `charts/quota-controller/values.yaml` (add `consumerAPIExportName: quota-consumer`)
- Modify: `charts/quota-controller/templates/deployment.yaml` (pass `--consumer-api-export-name`)
- Modify: `charts/quota-controller/templates/rbac.yaml` (extend `apiexports`/`apiexports/content` rules to also cover resourceName `quota-consumer` for the controller role)
- Modify: `charts/quota-controller/files/` (ship the new export + schema YAMLs the way the 1a files are shipped, if the chart carries them — mirror the existing layout)
- Modify: `test/e2e/quota_test.go` (self-service scenarios) and `test/e2e/suite_test.go` (apply quota-consumer export + a consumer-identity kubeconfig helper if absent)

**Interfaces:**
- Consumes: everything above; spec §15 e2e list.

- [ ] **Step 1: Chart changes**

`values.yaml`: under the top-level `apiExportName` add:

```yaml
# Name of the quota-consumer APIExport (QuotaClaim self-service surface).
consumerAPIExportName: quota-consumer
```

`templates/deployment.yaml` args: add `- --consumer-api-export-name={{ .Values.consumerAPIExportName }}`.

`templates/rbac.yaml`: wherever the controller ClusterRole grants `apiexports` and `apiexports/content` with `resourceNames: ["quota-provider"]` (rendered names may use `.Values.apiExportName`), extend the list: `resourceNames: [{{ .Values.apiExportName | quote }}, {{ .Values.consumerAPIExportName | quote }}]`. The webhook role needs NO consumer-export access (it never reads claims).

Verify: `helm template test charts/quota-controller | grep -A2 "consumer-api-export-name"` renders the arg, and the rendered ClusterRole shows both resourceNames.

- [ ] **Step 2: e2e — extend the suite**

Add to `test/e2e/quota_test.go` a `Describe("self-service", Ordered, ...)` implementing spec §15's self-service list, reusing the suite's workspace/binding/identity helpers. The scenarios (each an `It`, in order):

1. **Visibility:** after the consumer binds the service export AND the quota-consumer export, `Eventually` a `QuotaClaim` named `qc-<resource>-<id8>` exists in the consumer workspace with `status.effectiveLimit == defaultLimit` (discovery sweep interval bounds the Eventually timeout — use the suite's standard eventually timeout).
2. **Auto-approve:** consumer updates `spec.requestedLimit` to a value ≤ `autoApproveCeiling` → `Eventually` claim `status.phase == Approved` and `status.effectiveLimit == requested`; consumer can then create objects up to the new limit and the (new limit + 1)th CREATE is denied with `consumption quota exceeded`.
3. **Manual approval:** consumer requests above the ceiling → `Eventually` claim `status.phase == Pending`; a `QuotaGrant` exists in the provider workspace with `decision: Pending`; provider (provider-workspace identity) sets `decision: Approved, grantedLimit: <n>` → `Eventually` claim `status.phase == Approved` and enforcement honors the new limit.
4. **Rejection:** provider sets `decision: Rejected` on a fresh pending grant → claim `status.phase == Rejected` and enforcement stays at the default.
5. **MPP integrity:** as the consumer-workspace identity: `create` of a new QuotaClaim → Forbidden; `delete` of the pre-created claim → Forbidden; `status().update` → Forbidden; `update` of `spec.requestedLimit` → Succeeds. (Formalizes the Task 1 spike.)
6. **Identity separation:** two providers exporting the same group/resource → grants only affect the matching identityHash ledger (reuse the 1a two-provider fixture if present; otherwise assert via distinct claims `qc-<resource>-<idA8>` vs `qc-<resource>-<idB8>`).

The suite bootstrap must additionally: apply `config/kcp/apiexport-quota-consumer.yaml` + the two new schemas in the quota-ctrl workspace, create the consumer's `APIBinding` to `quota-consumer`, and accept nothing extra (no permissionClaims on that export).

- [ ] **Step 3: Run e2e**

Run: `make test-e2e`
Expected: full suite PASS (1a scenarios + the 6 new ones).

- [ ] **Step 4: Stage**

```bash
git add charts/ test/e2e/
```
Suggested message: `gitcs 'feat(chart,e2e): ship quota-consumer export and cover self-service end-to-end'`

---

### Task 10: Docs — README roadmap, getting-started, spec status

**Files:**
- Modify: `README.md` (roadmap row for 1b → implemented once e2e is green)
- Modify: `docs/getting-started.md` (consumer walkthrough: bind quota-consumer, read claim, request, approve)
- Modify: `docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md` (implementation-status note: 1b implemented)

- [ ] **Step 1: README roadmap**

Change the Iteration 1b row status to `✅ **Implemented**` with the same phrasing style as the 1a row, and update the intro sentence above the table ("Only enforcement (Iteration 1a) is implemented today" → both 1a and 1b implemented; Iteration 2 designed-for).

- [ ] **Step 2: Getting-started**

Add a "Self-service quota (consumer)" section after the existing consumer walkthrough: bind `quota-consumer` (APIBinding YAML, no permission claims), `kubectl get quotaclaims` to see the effective limit, edit `spec.requestedLimit`, watch `status.phase`; and a provider subsection: `kubectl get quotagrants`, approve by setting `spec.decision: Approved` + `spec.grantedLimit`. Include exact YAML for the APIBinding and one claim-edit example consistent with Task 3's export name and Task 6's naming scheme.

- [ ] **Step 3: Spec status note**

Update the header note (lines 11–17) to record 1b as implemented (date + plan link), leaving Iteration 2 designed-for.

- [ ] **Step 4: Stage**

```bash
git add README.md docs/
```
Suggested message: `gitcs 'docs: mark iteration 1b implemented; consumer self-service walkthrough'`

---

## Self-review notes (kept for executors)

- **Spec coverage:** R8 (Task 7 claim reconciler), R9 auto+manual (Tasks 6/7/9), R10 (Task 3 MPP + Task 9 scenario 5), R11 visibility (Tasks 7/8 ensure+discovery, Task 9 scenario 1), §9 resolution (Tasks 4/5), §14.2/#5 verification (Task 1), ADR-005 (Task 1), identity rotation posture (no code needed beyond stale-ref fail-closed paths in Tasks 7/8 — claims/grants keyed on identity simply stop matching; `IdentityRotated` condition intentionally deferred to the CQ reconciler follow-up if rotation is ever observed in practice — noted in spike notes).
- **Known adaptation points (spike-gated):** Task 3 MPP YAML shape; Task 8 `claimDiscovery.sweep` source and the `APIBinding.Spec.Reference` field path (SDK-version dependent). Everything else is deterministic.
- **Type-consistency check:** `GovernedIdentity`, `GrantDecision`/`ClaimPhase` constants, `ClaimName`/`GrantName`, factory signatures, and `PolicyIndex` methods are referenced identically across Tasks 2, 4–9.
