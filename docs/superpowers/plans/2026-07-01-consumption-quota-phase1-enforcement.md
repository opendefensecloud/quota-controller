# Consumption Quota — Phase 1 (Enforcement Core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship strict, no-overshoot, count-based consumption quotas for kcp `APIExport` resources, enforced by an external admission webhook with CAS-reserved accounting — provider-set default limits only (no self-service; that is Phase 2).

> **Vocabulary (reconciled with the spec):** this plan's **Phase 1** is the spec's **Iteration 1a** (count enforcement); its **Phase 2** is the spec's **Iteration 1b** (self-service). Phase 2 here is **not** the spec's **Iteration 2** (aggregate/property quotas). The README "Status / Roadmap" holds the canonical status. This plan covers Phase 1 / Iteration 1a only.

**Architecture:** A `ConsumptionQuota` policy (created by a provider alongside its `APIExport`) drives a `ValidatingWebhookConfiguration` installed into the provider workspace that intercepts `CREATE` of the governed type across all consumer workspaces. The webhook reserves a slot in a per-`(consumer, resource-identity)` `QuotaUsage` object using optimistic-concurrency (compare-and-swap); an accounting reconciler keeps the real count (`confirmed`) honest by watching the governed objects across consumers via the provider's `APIExport` virtual workspace. See the design spec: `docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md` (§§4–11) and `architecture/ADR-001-external-cas-quota-enforcement.md`.

**Tech Stack:** Go 1.26; `sigs.k8s.io/controller-runtime` v0.23.x + `sigs.k8s.io/multicluster-runtime` v0.23.x; `github.com/kcp-dev/multicluster-provider` v0.7.x (+ `/client`); `github.com/kcp-dev/sdk` v0.31.x; `github.com/kcp-dev/logicalcluster/v3`; Ginkgo v2 + Gomega; envtest; Helm; Nix flake + direnv; `go-task`; `golangci-lint`.

## Global Constraints

- **Module path:** `go.opendefense.cloud/quota-controller`. **Go:** the `go.mod` directive is `1.26.3` (matches the installed toolchain; avoids a toolchain download). The nix dev shell may provide a newer 1.26.x — fine.
- **Dev shell:** `flake.nix` uses the org `dev-kit` flake (`github:opendefensecloud/dev-kit`, `dev-kit.lib.mkShell { goVersion = "1.26.4"; }`) exactly as `dependency-controller/flake.nix` does — it supplies `controller-gen`, `setup-envtest`, `golangci-lint`, `go-task`, `gopls`. The tool shell is NOT auto-inside it: run flake-only tools via `nix develop -c <cmd>`, or fall back to `go run sigs.k8s.io/controller-tools/cmd/controller-gen@<pinned>` / `setup-envtest`.
- **License header:** every Go file starts with dep-ctrl's header, adapted:
  `// Copyright 2026 BWI GmbH and Quota Controller contributors` then `// SPDX-License-Identifier: Apache-2.0`.
- **API group/version:** `quota.opendefense.cloud/v1alpha1`.
- **Reuse, don't reinvent:** the kcp glue (VW-URL discovery from `APIExportEndpointSlice`, `mcmanager`/`multicluster-provider` setup, workspace path→logicalcluster resolver, the webhook-installer skeleton) already exists in the sibling repo `dependency-controller` (`go.opendefense.cloud/dependency-controller`). Adapt those files by the exact references given per task rather than writing from scratch.
- **Testing:** Go tests use Ginkgo (BDD) + Gomega. Integration tests use envtest via `controller-runtime`. Run the race detector on the pure-logic packages.
- **Commits:** use the `gitcs` alias (signed; hardware token required). The commit step is run by the human operator. Never use `--no-gpg-sign`.
- **Branch:** work on `feature/no-ref/consumption-quota` (already checked out). Never commit to `main`.
- **Secrets:** never commit kubeconfigs/keys; `.secrets/`, `.env*`, `*.key`, `*.pem` are already git-ignored.
- **Naming:** enforcement resource identity is always the tuple `(group, resource, identityHash)`; `QuotaUsage` objects and the in-memory limit registry are keyed by it (spec §6).
- **Phase 1 scope guard:** `ConsumptionQuota.spec.autoApproveCeiling`, `QuotaGrant`, `QuotaClaim`, and the second (consumer-facing) `APIExport` are **Phase 2** — do not build them here. Phase 1 enforces `defaultLimit` only.

---

## Deployment & Environment (sysdemo)

- **Target cluster:** the already-running local kind cluster **`sysdemo`** (`kubectl --context kind-sysdemo`). The controller + webhook run here as Deployments via the Helm chart (Task 11).
- **kcp kubeconfig:** front-proxy admin at `/Users/cyrill/Documents/Repos/opendefense-gl/cat/demo/flux-sysdemo-26.7/.secrets/kcp-admin-fp-sysdemo.kubeconfig` (currently points at the root shard). Use `KUBECONFIG=<that>` for all kcp `kubectl`/`kubectl ws` operations. **Never commit it** (it's under a `.secrets/` path).
- **Quota-ctrl workspace:** the `quota-provider` `APIExport`, its `APIResourceSchema`s, and the internal `QuotaUsage` CRD live in kcp workspace **`root:cloud-api-system`** (`kubectl ws root:cloud-api-system`). The controller/webhook connect to this workspace's virtual workspace.
- **e2e policy for this iteration:** **write** the e2e suite (Task 12) mirroring `dependency-controller/test/e2e/`, but **do not run it**. Instead deploy to `sysdemo` + `root:cloud-api-system` and smoke-test manually (Task 12 revised steps).
- **Reference deployment:** follow how `dependency-controller` is deployed (its Helm chart values, cert-manager wiring, and the workspace bootstrap in its `docs/getting-started.md`).

---

## File Structure

```
quota-controller/
├── flake.nix .envrc Taskfile.yml .pre-commit-config.yaml .golangci.yaml   # Task 1
├── go.mod go.sum
├── api/v1alpha1/
│   ├── groupversion_info.go            # scheme registration          (Task 2)
│   ├── consumptionquota_types.go       # ConsumptionQuota             (Task 2)
│   ├── quotausage_types.go             # QuotaUsage                   (Task 2)
│   └── zz_generated.deepcopy.go        # generated                    (Task 2)
├── internal/
│   ├── identity/key.go                 # UsageKey + deterministic name (Task 3)
│   ├── accounting/accounting.go        # pure reserve/sweep/fold/used  (Task 3)
│   ├── quota/store.go                  # QuotaUsage CAS wrapper         (Task 4)
│   ├── registry/registry.go           # (group,resource,identity)->limit (Task 5)
│   ├── kcp/                            # VW discovery, resolver (adapt dep-ctrl) (Task 8)
│   ├── controller/
│   │   ├── consumptionquota_controller.go  # registry feeder + identity resolve + installer (Task 6,7,9)
│   │   ├── usage_reconciler.go             # confirmed/fold/sweep         (Task 8)
│   │   └── webhook_installer.go            # VWC install (adapt dep-ctrl)  (Task 9)
│   └── webhook/
│       └── creation_validator.go      # admission.Handler (CREATE)      (Task 7)
├── cmd/controller/main.go             # controller + reconciler binary   (Task 10)
├── cmd/webhook/main.go                # admission webhook binary         (Task 10)
├── config/kcp/                        # APIResourceSchemas + APIExport   (Task 11)
├── charts/quota-controller/           # Helm chart                       (Task 11)
└── test/                              # e2e fixtures + suite             (Task 12)
```

---

## Task 1: Repository scaffolding & dev environment

**Files:**
- Create: `go.mod`, `flake.nix`, `.envrc`, `Taskfile.yml`, `.pre-commit-config.yaml`, `.golangci.yaml`, `README.md`, `.gitignore` (already exists — extend if needed)

**Interfaces:**
- Produces: a compiling, lint-clean empty module with `task test`, `task lint`, `task run` targets; module path `go.opendefense.cloud/quota-controller`.

- [ ] **Step 1: Initialize the Go module**

Run:
```bash
cd /Users/cyrill/Documents/Repos/opendefense-gl/personal/quota
go mod init go.opendefense.cloud/quota-controller
go mod edit -go=1.26.4
```

- [ ] **Step 2: Create `flake.nix` dev shell**

Create `flake.nix`:
```nix
{
  description = "quota-controller dev shell";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";
  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let pkgs = import nixpkgs { inherit system; };
      in {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26 gopls gotools go-tools golangci-lint
            go-task kubernetes-helm kind kubectl
            kube-controller-tools            # controller-gen
            setup-envtest
          ];
        };
      });
}
```

- [ ] **Step 3: Create `.envrc`**

Create `.envrc`:
```bash
use flake
```
Run: `direnv allow` (or `nix develop`).

- [ ] **Step 4: Create `Taskfile.yml`**

Create `Taskfile.yml`:
```yaml
version: "3"
tasks:
  test:
    desc: Run the test suite
    cmds:
      - go test -race ./...
  lint:
    desc: Run linters
    cmds:
      - golangci-lint run
  run:
    desc: Run the controller locally (needs KUBECONFIG to a kcp quota-ctrl workspace)
    cmds:
      - go run ./cmd/controller
  generate:
    desc: Regenerate deepcopy + CRDs
    cmds:
      - controller-gen object:headerFile=/dev/null paths=./api/...
      - controller-gen crd paths=./api/... output:crd:dir=config/crds
```

- [ ] **Step 5: Create `.golangci.yaml` and `.pre-commit-config.yaml`**

Create `.golangci.yaml` (copy the enabled linter set from `dependency-controller/.golangci.yaml`, adjusting the module path in any `depguard`/`goimports` local-prefix settings to `go.opendefense.cloud/quota-controller`).

Create `.pre-commit-config.yaml`:
```yaml
repos:
  - repo: https://github.com/golangci/golangci-lint
    rev: v1.64.5
    hooks: [{ id: golangci-lint }]
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v5.0.0
    hooks: [{ id: end-of-file-fixer }, { id: trailing-whitespace }, { id: check-yaml }]
```
Run: `pre-commit install`.

- [ ] **Step 6: Create `README.md`**

Create `README.md` with a "Getting started" section: `direnv allow` / `nix develop`, `task test`, `task lint`, and a one-paragraph project overview linking to the spec and ADRs.

- [ ] **Step 7: Verify the shell builds and tooling is present**

Run: `go build ./... && task lint`
Expected: no packages yet → build succeeds with no output; lint reports no issues.

- [ ] **Step 8: Commit**

```bash
git add flake.nix .envrc Taskfile.yml .pre-commit-config.yaml .golangci.yaml README.md go.mod
gitcs 'chore: scaffold quota-controller repo and dev shell'
```

---

## Task 2: API types — `ConsumptionQuota` and `QuotaUsage`

**Files:**
- Create: `api/v1alpha1/groupversion_info.go`, `api/v1alpha1/consumptionquota_types.go`, `api/v1alpha1/quotausage_types.go`
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`

**Interfaces:**
- Produces:
  - `GovernedResource struct { APIExportName, Group, Version, Resource string }`
  - `ConsumptionQuota` with `Spec{ Governed GovernedResource; By string; DefaultLimit int32; AutoApproveCeiling *int32 }`, `Status{ IdentityHash string; Conditions []metav1.Condition }`
  - `QuotaUsage` (cluster-scoped) with `Status{ Confirmed int32; Reservations []Reservation }`, `Reservation struct { Key string; ExpiresAt metav1.Time }`
  - `GroupVersion = schema.GroupVersion{Group: "quota.opendefense.cloud", Version: "v1alpha1"}`, `AddToScheme`

- [ ] **Step 1: Create `groupversion_info.go`**

Create `api/v1alpha1/groupversion_info.go` (adapt `dependency-controller/api/v1alpha1/groupversion_info.go`, changing the group to `quota.opendefense.cloud`):
```go
// +kubebuilder:object:generate=true
// +groupName=quota.opendefense.cloud
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "quota.opendefense.cloud", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
```

- [ ] **Step 2: Create `consumptionquota_types.go`**

Create `api/v1alpha1/consumptionquota_types.go`:
```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// ConsumptionQuota caps how many instances of a governed APIExport resource each
// consumer workspace may create. Created by a provider alongside its APIExport.
type ConsumptionQuota struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ConsumptionQuotaSpec   `json:"spec"`
	Status            ConsumptionQuotaStatus `json:"status,omitempty"`
}

type ConsumptionQuotaSpec struct {
	// Governed is the exported resource type this quota caps.
	Governed GovernedResource `json:"governed"`
	// By selects the accounting mode. Phase 1 supports only "Count".
	// +kubebuilder:validation:Enum=Count
	// +kubebuilder:default=Count
	By string `json:"by"`
	// DefaultLimit applies to every consumer workspace with no grant.
	// +kubebuilder:validation:Minimum=0
	DefaultLimit int32 `json:"defaultLimit"`
	// AutoApproveCeiling is reserved for Phase 2 (self-service) and ignored in Phase 1.
	// +optional
	AutoApproveCeiling *int32 `json:"autoApproveCeiling,omitempty"`
}

// GovernedResource identifies an exported resource type by its APIExport and GVR.
type GovernedResource struct {
	// APIExportName is the APIExport in the same workspace as this policy.
	// +kubebuilder:validation:MinLength=1
	APIExportName string `json:"apiExportName"`
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
}

type ConsumptionQuotaStatus struct {
	// IdentityHash is resolved by the controller from the governed APIExport's
	// status.identityHash. It disambiguates same-named resources from different providers.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ConsumptionQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConsumptionQuota `json:"items"`
}

func init() { SchemeBuilder.Register(&ConsumptionQuota{}, &ConsumptionQuotaList{}) }
```

- [ ] **Step 3: Create `quotausage_types.go`**

Create `api/v1alpha1/quotausage_types.go`:
```go
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaUsage is the internal, authoritative accounting object for one
// (consumerCluster, group, resource, identityHash). Not exported to providers or consumers.
type QuotaUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaUsageSpec   `json:"spec"`
	Status            QuotaUsageStatus `json:"status,omitempty"`
}

// QuotaUsageSpec records the identity this ledger belongs to (for humans/debugging;
// enforcement keys by object name). All fields are set once at creation.
type QuotaUsageSpec struct {
	Consumer     string `json:"consumer"`
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	IdentityHash string `json:"identityHash"`
}

type QuotaUsageStatus struct {
	// Confirmed is the real live object count, owned by the accounting reconciler.
	// +kubebuilder:validation:Minimum=0
	Confirmed int32 `json:"confirmed"`
	// Reservations are admits allowed but not yet observed as live objects.
	// +optional
	Reservations []Reservation `json:"reservations,omitempty"`
}

// Reservation is an in-flight admit slot with a TTL.
type Reservation struct {
	// Key is the reserved object's namespace/name.
	Key string `json:"key"`
	// ExpiresAt is when the sweep may reclaim this reservation if unfulfilled.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// +kubebuilder:object:root=true
type QuotaUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaUsage `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaUsage{}, &QuotaUsageList{}) }
```

- [ ] **Step 4: Generate deepcopy and CRDs**

Run: `task generate`
Expected: creates `api/v1alpha1/zz_generated.deepcopy.go` and `config/crds/*.yaml` with no errors.

- [ ] **Step 5: Verify it compiles**

Run: `go build ./api/...`
Expected: success, no output.

- [ ] **Step 6: Commit**

```bash
git add api config/crds
gitcs 'feat: add ConsumptionQuota and QuotaUsage API types'
```

---

## Task 3: Identity key + pure accounting logic

This is the heart of the strict guarantee (spec §7). Keep it a pure package with no Kubernetes client, so the reservation math is exhaustively unit-testable.

**Files:**
- Create: `internal/identity/key.go`, `internal/identity/key_test.go`
- Create: `internal/accounting/accounting.go`, `internal/accounting/accounting_test.go`

**Interfaces:**
- Produces:
  - `identity.UsageKey struct { Cluster, Group, Resource, IdentityHash string }`; `func (k UsageKey) ObjectName() string` (deterministic DNS-1123 name, `qu-` + hex sha256 prefix)
  - `accounting.Used(st v1alpha1.QuotaUsageStatus, now time.Time) int32`
  - `accounting.Reserve(st v1alpha1.QuotaUsageStatus, objKey string, limit int32, now time.Time, ttl time.Duration) (v1alpha1.QuotaUsageStatus, bool)` — returns updated status and whether the slot was granted; idempotent for an already-present live `objKey`.
  - `accounting.Sweep(st v1alpha1.QuotaUsageStatus, now time.Time) v1alpha1.QuotaUsageStatus`
  - `accounting.FoldConfirmed(st v1alpha1.QuotaUsageStatus, trueCount int32, liveKeys sets.Set[string]) v1alpha1.QuotaUsageStatus`

- [ ] **Step 1: Write the failing test for `UsageKey.ObjectName`**

Create `internal/identity/key_test.go`:
```go
package identity_test

import (
	"testing"

	"go.opendefense.cloud/quota-controller/internal/identity"
)

func TestObjectNameDeterministicAndValid(t *testing.T) {
	k := identity.UsageKey{Cluster: "root:c1", Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc"}
	got := k.ObjectName()
	if got != k.ObjectName() {
		t.Fatal("ObjectName not deterministic")
	}
	if len(got) == 0 || len(got) > 63 || got[:3] != "qu-" {
		t.Fatalf("invalid object name %q", got)
	}
	other := identity.UsageKey{Cluster: "root:c2", Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc"}
	if other.ObjectName() == got {
		t.Fatal("distinct keys collided")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/identity/...`
Expected: FAIL — package/`UsageKey` undefined.

- [ ] **Step 3: Implement `internal/identity/key.go`**

```go
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// UsageKey identifies one accounting ledger: a consumer workspace and a governed
// resource identity (spec §6.4).
type UsageKey struct {
	Cluster      string
	Group        string
	Resource     string
	IdentityHash string
}

// ObjectName returns a deterministic, DNS-1123-valid cluster-scoped name for the
// QuotaUsage object backing this key.
func (k UsageKey) ObjectName() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s", k.Cluster, k.Group, k.Resource, k.IdentityHash)))
	return "qu-" + hex.EncodeToString(sum[:])[:32]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/identity/...`
Expected: PASS.

- [ ] **Step 5: Write the failing tests for accounting**

Create `internal/accounting/accounting_test.go`:
```go
package accounting_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"
)

var t0 = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

func res(key string, exp time.Time) v1alpha1.Reservation {
	return v1alpha1.Reservation{Key: key, ExpiresAt: metav1.NewTime(exp)}
}

func TestUsedCountsConfirmedPlusLiveReservations(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{
		Confirmed:    2,
		Reservations: []v1alpha1.Reservation{res("ns/a", t0.Add(time.Minute)), res("ns/b", t0.Add(-time.Minute))},
	}
	if got := accounting.Used(st, t0); got != 3 { // 2 confirmed + 1 live (b expired)
		t.Fatalf("Used = %d, want 3", got)
	}
}

func TestReserveGrantsWhenBelowLimit(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{Confirmed: 2}
	out, ok := accounting.Reserve(st, "ns/new", 3, t0, time.Minute)
	if !ok {
		t.Fatal("expected reservation granted")
	}
	if len(out.Reservations) != 1 || out.Reservations[0].Key != "ns/new" {
		t.Fatalf("reservation not recorded: %+v", out.Reservations)
	}
}

func TestReserveDeniesAtLimit(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{Confirmed: 3}
	if _, ok := accounting.Reserve(st, "ns/new", 3, t0, time.Minute); ok {
		t.Fatal("expected reservation denied at limit")
	}
}

func TestReserveIsIdempotentForSameKey(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{Confirmed: 2, Reservations: []v1alpha1.Reservation{res("ns/x", t0.Add(time.Minute))}}
	out, ok := accounting.Reserve(st, "ns/x", 3, t0, time.Minute)
	if !ok || len(out.Reservations) != 1 {
		t.Fatalf("retry of same key must not double-count: ok=%v n=%d", ok, len(out.Reservations))
	}
}

func TestSweepDropsExpired(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{Reservations: []v1alpha1.Reservation{
		res("ns/live", t0.Add(time.Minute)), res("ns/dead", t0.Add(-time.Second)),
	}}
	out := accounting.Sweep(st, t0)
	if len(out.Reservations) != 1 || out.Reservations[0].Key != "ns/live" {
		t.Fatalf("sweep wrong result: %+v", out.Reservations)
	}
}

func TestFoldConfirmedRemovesObservedReservations(t *testing.T) {
	st := v1alpha1.QuotaUsageStatus{Confirmed: 1, Reservations: []v1alpha1.Reservation{
		res("ns/seen", t0.Add(time.Minute)), res("ns/pending", t0.Add(time.Minute)),
	}}
	out := accounting.FoldConfirmed(st, 2, sets.New("ns/seen"))
	if out.Confirmed != 2 {
		t.Fatalf("confirmed = %d, want 2", out.Confirmed)
	}
	if len(out.Reservations) != 1 || out.Reservations[0].Key != "ns/pending" {
		t.Fatalf("fold did not drop observed reservation: %+v", out.Reservations)
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/accounting/...`
Expected: FAIL — `accounting` undefined.

- [ ] **Step 7: Implement `internal/accounting/accounting.go`**

```go
// Package accounting holds the pure reservation math for QuotaUsage. No Kubernetes
// client — this is where the strict, no-overshoot guarantee is defined and tested.
package accounting

import (
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
)

// Used = confirmed + live (non-expired) reservations (spec §7).
func Used(st v1alpha1.QuotaUsageStatus, now time.Time) int32 {
	live := int32(0)
	for _, r := range st.Reservations {
		if r.ExpiresAt.Time.After(now) {
			live++
		}
	}
	return st.Confirmed + live
}

// Reserve grants a slot for objKey iff Used < limit. Idempotent: if objKey is already
// a live reservation it returns the status unchanged with ok=true (admission retry).
func Reserve(st v1alpha1.QuotaUsageStatus, objKey string, limit int32, now time.Time, ttl time.Duration) (v1alpha1.QuotaUsageStatus, bool) {
	for _, r := range st.Reservations {
		if r.Key == objKey && r.ExpiresAt.Time.After(now) {
			return st, true
		}
	}
	if Used(st, now) >= limit {
		return st, false
	}
	out := *st.DeepCopy()
	out.Reservations = append(out.Reservations, v1alpha1.Reservation{
		Key:       objKey,
		ExpiresAt: metaTime(now.Add(ttl)),
	})
	return out, true
}

// Sweep removes expired reservations (orphaned admits whose create never landed).
func Sweep(st v1alpha1.QuotaUsageStatus, now time.Time) v1alpha1.QuotaUsageStatus {
	out := *st.DeepCopy()
	kept := out.Reservations[:0]
	for _, r := range out.Reservations {
		if r.ExpiresAt.Time.After(now) {
			kept = append(kept, r)
		}
	}
	out.Reservations = kept
	return out
}

// FoldConfirmed sets Confirmed to the true live count and drops reservations whose
// object has now been observed (liveKeys) — the fulfilled-reservation hand-off (spec §7.2).
func FoldConfirmed(st v1alpha1.QuotaUsageStatus, trueCount int32, liveKeys sets.Set[string]) v1alpha1.QuotaUsageStatus {
	out := *st.DeepCopy()
	out.Confirmed = trueCount
	kept := out.Reservations[:0]
	for _, r := range out.Reservations {
		if !liveKeys.Has(r.Key) {
			kept = append(kept, r)
		}
	}
	out.Reservations = kept
	return out
}
```

Add the small helper in the same file:
```go
import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

func metaTime(t time.Time) metav1.Time { return metav1.NewTime(t) }
```
(Merge the `metav1` import into the import block.)

- [ ] **Step 8: Run tests (with race) to verify they pass**

Run: `go test -race ./internal/accounting/... ./internal/identity/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/identity internal/accounting
gitcs 'feat: pure CAS reservation accounting + identity keying'
```

---

## Task 4: `QuotaUsage` CAS store

Wraps the pure logic in a get-or-create + compare-and-swap retry loop against a real client. Tested with envtest so the 409-conflict behavior is real.

**Files:**
- Create: `internal/quota/store.go`, `internal/quota/store_test.go`
- Create: `internal/quota/suite_test.go` (envtest bootstrap — adapt `dependency-controller/internal/controller/suite_test.go`)

**Interfaces:**
- Consumes: `identity.UsageKey`, `accounting.Reserve`, `v1alpha1.QuotaUsage`.
- Produces:
  - `type Store struct { Client client.Client; TTL time.Duration; Now func() time.Time }`
  - `func (s *Store) Reserve(ctx context.Context, key identity.UsageKey, objKey string, limit int32) (allowed bool, err error)`
  - `func (s *Store) Apply(ctx context.Context, key identity.UsageKey, mutate func(v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus) error` (CAS helper reused by the reconciler in Task 8)

- [ ] **Step 1: Create the envtest suite bootstrap**

Create `internal/quota/suite_test.go` by adapting `dependency-controller/internal/controller/suite_test.go`: start `envtest.Environment` with `CRDDirectoryPaths: []string{"../../config/crds"}`, register `v1alpha1.AddToScheme`, expose a package-level `k8sClient client.Client`. Use Ginkgo's `BeforeSuite`/`AfterSuite`.

- [ ] **Step 2: Write the failing test for get-or-create + reserve**

Create `internal/quota/store_test.go`:
```go
package quota_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"
)

var _ = Describe("Store.Reserve", func() {
	key := identity.UsageKey{Cluster: "root:c1", Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("creates the ledger and grants up to the limit, then denies", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		ctx := context.Background()

		ok1, err := s.Reserve(ctx, key, "ns/a", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok1).To(BeTrue())

		ok2, err := s.Reserve(ctx, key, "ns/b", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok2).To(BeTrue())

		ok3, err := s.Reserve(ctx, key, "ns/c", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok3).To(BeFalse()) // at limit
	})
})
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/quota/...`
Expected: FAIL — `quota.Store` undefined.

- [ ] **Step 4: Implement `internal/quota/store.go`**

```go
// Package quota wraps the pure accounting logic in a compare-and-swap loop against
// the QuotaUsage objects in the quota-ctrl workspace (spec §7.1).
package quota

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"
	"go.opendefense.cloud/quota-controller/internal/identity"
)

const maxCASRetries = 8

type Store struct {
	Client client.Client
	TTL    time.Duration
	Now    func() time.Time
}

// Reserve grants a slot for objKey under limit, using get-or-create + CAS retry.
func (s *Store) Reserve(ctx context.Context, key identity.UsageKey, objKey string, limit int32) (bool, error) {
	allowed := false
	err := s.Apply(ctx, key, func(st v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus {
		out, ok := accounting.Reserve(st, objKey, limit, s.Now(), s.TTL)
		allowed = ok
		return out
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
}

// Apply get-or-creates the QuotaUsage for key and applies mutate to its status under
// optimistic concurrency, retrying on conflict.
func (s *Store) Apply(ctx context.Context, key identity.UsageKey, mutate func(v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus) error {
	name := key.ObjectName()
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		u := &v1alpha1.QuotaUsage{}
		err := s.Client.Get(ctx, client.ObjectKey{Name: name}, u)
		switch {
		case apierrors.IsNotFound(err):
			u = &v1alpha1.QuotaUsage{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: v1alpha1.QuotaUsageSpec{
					Consumer: key.Cluster, Group: key.Group,
					Resource: key.Resource, IdentityHash: key.IdentityHash,
				},
			}
			if cerr := s.Client.Create(ctx, u); cerr != nil {
				if apierrors.IsAlreadyExists(cerr) {
					continue // someone else created it; re-get
				}
				return cerr
			}
		case err != nil:
			return err
		}

		u.Status = mutate(u.Status)
		if err := s.Client.Status().Update(ctx, u); err != nil {
			if apierrors.IsConflict(err) {
				continue // CAS lost; retry
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("quota: CAS exhausted after %d attempts for %s", maxCASRetries, name)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/quota/...`
Expected: PASS (envtest starts a real apiserver; the status subresource enforces `resourceVersion` CAS).

- [ ] **Step 6: Add a concurrency test proving no overshoot**

Append to `internal/quota/store_test.go`:
```go
var _ = Describe("Store.Reserve under concurrency", func() {
	It("never grants more than the limit for parallel reservations", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		key := identity.UsageKey{Cluster: "root:c2", Group: "s3.example.com", Resource: "buckets", IdentityHash: "h2"}
		const limit, n = 5, 20
		granted := make(chan bool, n)
		for i := 0; i < n; i++ {
			go func(i int) {
				defer GinkgoRecover()
				ok, err := s.Reserve(context.Background(), key, fmt.Sprintf("ns/o%d", i), limit)
				Expect(err).NotTo(HaveOccurred())
				granted <- ok
			}(i)
		}
		count := 0
		for i := 0; i < n; i++ {
			if <-granted {
				count++
			}
		}
		Expect(count).To(Equal(limit)) // exactly limit granted, never more
	})
})
```
Add `"fmt"` to the test imports.

- [ ] **Step 7: Run the concurrency test**

Run: `go test -race ./internal/quota/...`
Expected: PASS — exactly 5 granted out of 20.

- [ ] **Step 8: Commit**

```bash
git add internal/quota
gitcs 'feat: QuotaUsage CAS store with no-overshoot concurrency test'
```

---

## Task 5: Limit registry

An in-memory index the webhook consults on the hot path (spec §9). Phase 1: limit = `defaultLimit` (grants are Phase 2). Fed by the `ConsumptionQuota` controller (Task 6).

**Files:**
- Create: `internal/registry/registry.go`, `internal/registry/registry_test.go`

**Interfaces:**
- Produces:
  - `type ResourceRef struct { Group, Resource, IdentityHash string }`
  - `type Registry struct { … }`, `func New() *Registry`
  - `func (r *Registry) Set(ref ResourceRef, defaultLimit int32)`; `func (r *Registry) Delete(ref ResourceRef)`
  - `func (r *Registry) LimitFor(cluster string, ref ResourceRef) (limit int32, ok bool)` (cluster param unused in Phase 1; present so Phase 2 grants slot in without a signature change)
  - `func (r *Registry) ByGroupResource(group, resource string) []ResourceRef` (webhook path→identity fallback lookup)

- [ ] **Step 1: Write the failing test**

Create `internal/registry/registry_test.go`:
```go
package registry_test

import (
	"testing"

	"go.opendefense.cloud/quota-controller/internal/registry"
)

func TestSetAndLimitFor(t *testing.T) {
	r := registry.New()
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
	r.Set(ref, 3)

	got, ok := r.LimitFor("root:c1", ref)
	if !ok || got != 3 {
		t.Fatalf("LimitFor = (%d,%v), want (3,true)", got, ok)
	}

	r.Delete(ref)
	if _, ok := r.LimitFor("root:c1", ref); ok {
		t.Fatal("expected no limit after delete")
	}
}

func TestByGroupResource(t *testing.T) {
	r := registry.New()
	r.Set(registry.ResourceRef{Group: "g", Resource: "r", IdentityHash: "a"}, 1)
	r.Set(registry.ResourceRef{Group: "g", Resource: "r", IdentityHash: "b"}, 2)
	if n := len(r.ByGroupResource("g", "r")); n != 2 {
		t.Fatalf("ByGroupResource len = %d, want 2", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/registry/...`
Expected: FAIL — `registry` undefined.

- [ ] **Step 3: Implement `internal/registry/registry.go`**

```go
// Package registry is the webhook's in-memory (group,resource,identity)->limit index,
// fed by the ConsumptionQuota controller (spec §9).
package registry

import "sync"

type ResourceRef struct {
	Group        string
	Resource     string
	IdentityHash string
}

type Registry struct {
	mu     sync.RWMutex
	limits map[ResourceRef]int32
}

func New() *Registry { return &Registry{limits: map[ResourceRef]int32{}} }

func (r *Registry) Set(ref ResourceRef, defaultLimit int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits[ref] = defaultLimit
}

func (r *Registry) Delete(ref ResourceRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.limits, ref)
}

// LimitFor returns the effective limit for a consumer. Phase 1: the default only.
// Phase 2 will consult per-consumer grants here (cluster is already in the signature).
func (r *Registry) LimitFor(_ string, ref ResourceRef) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.limits[ref]
	return l, ok
}

func (r *Registry) ByGroupResource(group, resource string) []ResourceRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ResourceRef
	for ref := range r.limits {
		if ref.Group == group && ref.Resource == resource {
			out = append(out, ref)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/registry
gitcs 'feat: in-memory limit registry keyed by resource identity'
```

---

## Task 6 + 7: `ConsumptionQuota` controller (identity resolve + registry feed) and Creation admission validator

These two are one reviewable deliverable: the controller populates the registry (and stamps identity), and the webhook consumes the registry to enforce. Tested together via the registry seam.

**Files:**
- Create: `internal/controller/consumptionquota_controller.go`
- Create: `internal/webhook/creation_validator.go`, `internal/webhook/creation_validator_test.go`

**Interfaces:**
- Consumes: `registry.Registry`, `registry.ResourceRef`, `quota.Store`, `identity.UsageKey`, `v1alpha1.ConsumptionQuota`, kcp `apisv1alpha2.APIExport`.
- Produces:
  - `controller.ConsumptionQuotaReconciler` (controller-runtime `Reconcile`) that: resolves `Governed` APIExport `status.identityHash`, writes it to `ConsumptionQuota.status.identityHash`, and calls `Registry.Set/Delete`.
  - `webhook.CreationValidator struct { Reg *registry.Registry; Store *quota.Store }` implementing `admission.Handler`, mounted at path `/validate/{group}/{resource}/{identityHash}`; `func (v *CreationValidator) Handle(ctx, req admission.Request) admission.Response`.

- [ ] **Step 1: Write the failing test for the validator**

Create `internal/webhook/creation_validator_test.go`:
```go
package webhook_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
	whook "go.opendefense.cloud/quota-controller/internal/webhook"
)

func createReq(cluster, group, resource, ns, name string) admission.Request {
	obj := map[string]any{
		"apiVersion": group + "/v1",
		"kind":       "Bucket",
		"metadata":   map[string]any{"name": name, "namespace": ns, "annotations": map[string]any{"kcp.io/cluster": cluster}},
	}
	raw, _ := json.Marshal(obj)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Resource:  metav1.GroupVersionResource{Group: group, Version: "v1", Resource: resource},
		Namespace: ns,
		Name:      name,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

// fakeStore lets us assert allow/deny without a cluster.
type fakeStore struct{ allow bool }

func (f fakeStore) Reserve(context.Context, ...any) (bool, error) { return f.allow, nil }

func TestValidatorDeniesWhenStoreDenies(t *testing.T) {
	reg := registry.New()
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
	reg.Set(ref, 3)
	v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
	// Bind the validator to identity h1 (as the mux path would):
	v.SetResource(ref)
	// Inject a deny decision by pointing Store at a fake reserve fn:
	v.ReserveFn = func(context.Context, string, string, int32) (bool, error) { return false, nil }

	resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))
	if resp.Allowed {
		t.Fatal("expected DENY when at limit")
	}
}

func TestValidatorAllowsWhenUnderLimit(t *testing.T) {
	reg := registry.New()
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
	reg.Set(ref, 3)
	v := &whook.CreationValidator{Reg: reg}
	v.SetResource(ref)
	v.ReserveFn = func(context.Context, string, string, int32) (bool, error) { return true, nil }

	resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))
	if !resp.Allowed {
		t.Fatal("expected ALLOW when under limit")
	}
}
```

> Design note: the validator is bound to one identity via the mux path (`SetResource`), and its reservation call is injectable (`ReserveFn`) so the handler is unit-testable without a cluster. In production `ReserveFn` defaults to `Store.Reserve`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/webhook/...`
Expected: FAIL — `CreationValidator` undefined.

- [ ] **Step 3: Implement `internal/webhook/creation_validator.go`**

```go
// Package webhook serves the CREATE admission that enforces consumption quotas (spec §7.1).
package webhook

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// CreationValidator enforces one governed resource identity. One instance is mounted
// per (group,resource,identityHash) at /validate/{group}/{resource}/{identityHash}.
type CreationValidator struct {
	Reg   *registry.Registry
	Store *quota.Store

	ref       registry.ResourceRef
	ReserveFn func(ctx context.Context, cluster, objKey string, limit int32) (bool, error)
}

// SetResource binds this validator to the identity parsed from the mount path.
func (v *CreationValidator) SetResource(ref registry.ResourceRef) {
	v.ref = ref
	if v.ReserveFn == nil {
		v.ReserveFn = func(ctx context.Context, cluster, objKey string, limit int32) (bool, error) {
			return v.Store.Reserve(ctx, identity.UsageKey{
				Cluster: cluster, Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash,
			}, objKey, limit)
		}
	}
}

func (v *CreationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("not a create")
	}
	cluster := clusterFromRequest(req)
	if cluster == "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("missing kcp.io/cluster on object"))
	}
	limit, ok := v.Reg.LimitFor(cluster, v.ref)
	if !ok {
		return admission.Allowed("no quota configured") // fail-open only for absence of policy
	}
	objKey := req.Namespace + "/" + req.Name
	allowed, err := v.ReserveFn(ctx, cluster, objKey, limit)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err) // fail-closed on error
	}
	if !allowed {
		return admission.Denied(fmt.Sprintf("consumption quota exceeded: at most %d %s per workspace", limit, v.ref.Resource))
	}
	return admission.Allowed("within quota")
}
```

Add the cluster extractor (mirror dep-ctrl's `deletion_validator.go` extraction of `kcp.io/cluster`) in the same file:
```go
import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func clusterFromRequest(req admission.Request) string {
	var pm metav1.PartialObjectMetadata
	src := req.Object.Raw
	if len(src) == 0 {
		src = req.OldObject.Raw
	}
	if err := json.Unmarshal(src, &pm); err != nil {
		return ""
	}
	return pm.Annotations["kcp.io/cluster"]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/webhook/...`
Expected: PASS.

- [ ] **Step 5: Implement the `ConsumptionQuota` controller**

Create `internal/controller/consumptionquota_controller.go`. It resolves the governed APIExport's identity and feeds the registry:
```go
package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// ConsumptionQuotaReconciler resolves identity and feeds the limit registry.
type ConsumptionQuotaReconciler struct {
	client.Client
	Reg *registry.Registry
}

func (r *ConsumptionQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cq := &v1alpha1.ConsumptionQuota{}
	if err := r.Get(ctx, req.NamespacedName, cq); err != nil {
		if apierrors.IsNotFound(err) {
			// On delete we can't know the identity here; the registry entry is removed
			// by resolving from a finalizer-captured status in production. For Phase 1,
			// rebuild-on-change keeps this simple: delete by group/resource+identity if known.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Resolve identityHash from the governed APIExport in the same workspace.
	ex := &apisv1alpha2.APIExport{}
	if err := r.Get(ctx, client.ObjectKey{Name: cq.Spec.Governed.APIExportName}, ex); err != nil {
		return ctrl.Result{}, err
	}
	id := ex.Status.IdentityHash
	if id == "" {
		return ctrl.Result{Requeue: true}, nil // identity not populated yet
	}

	if cq.Status.IdentityHash != id {
		cq.Status.IdentityHash = id
		if err := r.Status().Update(ctx, cq); err != nil {
			return ctrl.Result{}, err
		}
	}

	r.Reg.Set(registry.ResourceRef{
		Group: cq.Spec.Governed.Group, Resource: cq.Spec.Governed.Resource, IdentityHash: id,
	}, cq.Spec.DefaultLimit)
	return ctrl.Result{}, nil
}

func (r *ConsumptionQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ConsumptionQuota{}).Complete(r)
}
```

> Note for the implementer: robust registry cleanup on `ConsumptionQuota` delete needs the identity at delete time. Add a finalizer that captures `status.identityHash`, and on deletion call `Reg.Delete(...)` before removing the finalizer. Wire this in Task 9 alongside the webhook-installer finalizer (they share the same reconcile). Do not skip it — a stale registry entry would keep enforcing after policy removal.

- [ ] **Step 6: Verify build + tests**

Run: `go build ./... && go test ./internal/webhook/... ./internal/controller/...`
Expected: build OK; webhook tests PASS. (Controller has no unit test yet; envtest coverage comes in Task 12.)

- [ ] **Step 7: Commit**

```bash
git add internal/webhook internal/controller/consumptionquota_controller.go
gitcs 'feat: creation admission validator + ConsumptionQuota registry feeder'
```

---

## Task 8: Accounting reconciler (confirmed + fold + sweep)

Watches the governed objects across all consumers via the provider's service `APIExport` virtual workspace and keeps each `QuotaUsage.status.confirmed` honest, folding fulfilled reservations and sweeping expired ones (spec §7.2, §7.3).

**Files:**
- Create: `internal/controller/usage_reconciler.go`
- Create: `internal/kcp/vw.go` (VW-URL discovery from `APIExportEndpointSlice`) — adapt `dependency-controller/internal/kcp/endpointslice.go`

**Interfaces:**
- Consumes: `quota.Store.Apply`, `accounting.FoldConfirmed`, `accounting.Sweep`, `identity.UsageKey`, the provider service VW client (unstructured, multicluster).
- Produces:
  - `controller.UsageReconciler` that reconciles per governed object: recomputes the live count + live keys for `(cluster, group, resource, identity)` and calls `Store.Apply(FoldConfirmed(...))`.
  - A periodic sweeper that calls `Store.Apply(Sweep(...))` on all `QuotaUsage` objects every `resyncInterval`.

- [ ] **Step 1: Discover the VW URL (adapt dep-ctrl)**

Create `internal/kcp/vw.go` by adapting `dependency-controller/internal/kcp/endpointslice.go`: read the `APIExportEndpointSlice` for a given APIExport and return the virtual-workspace base URL. Keep the function signature `func VirtualWorkspaceURL(ctx context.Context, c client.Client, apiExportName string) (string, error)`.

- [ ] **Step 2: Write the failing envtest for fold/sweep via the reconciler's core function**

The reconciler's Kubernetes plumbing (multicluster informer over the provider VW) is integration-tested in Task 12. Here, unit-test the pure reconcile step that maps a set of observed objects to a `Store.Apply` call. Create `internal/controller/usage_reconciler_test.go`:
```go
package controller_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"
	"go.opendefense.cloud/quota-controller/internal/controller"
)

func TestReconcileConfirmedFoldsObservedKeys(t *testing.T) {
	// Given a ledger with a reservation for ns/a and one live object ns/a observed,
	// reconcile should set confirmed=1 and drop the ns/a reservation.
	start := v1alpha1.QuotaUsageStatus{
		Confirmed:    0,
		Reservations: []v1alpha1.Reservation{{Key: "ns/a", ExpiresAt: metav1.NewTime(time.Now().Add(time.Minute))}},
	}
	out := controller.ComputeConfirmed(start, []string{"ns/a"})
	if out.Confirmed != 1 || len(out.Reservations) != 0 {
		t.Fatalf("got confirmed=%d res=%d", out.Confirmed, len(out.Reservations))
	}
	_ = accounting.Used // keep import if trimmed
	_ = context.Background
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/controller/...`
Expected: FAIL — `ComputeConfirmed` undefined.

- [ ] **Step 4: Implement `internal/controller/usage_reconciler.go`**

```go
package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"
	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"
)

// ComputeConfirmed folds observed live object keys into a QuotaUsage status:
// confirmed = len(liveKeys), and any reservation whose object is now live is dropped.
func ComputeConfirmed(st v1alpha1.QuotaUsageStatus, liveKeys []string) v1alpha1.QuotaUsageStatus {
	set := sets.New(liveKeys...)
	return accounting.FoldConfirmed(st, int32(len(liveKeys)), set)
}

// UsageReconciler recomputes confirmed for the (cluster, resource-identity) of each
// changed governed object and periodically sweeps expired reservations.
type UsageReconciler struct {
	Store          *quota.Store
	Group          string
	Resource       string
	IdentityHash   string
	ResyncInterval time.Duration

	// ListLiveKeys returns the ns/name of every live governed object in a consumer
	// cluster (from the multicluster informer cache; wired in cmd/controller).
	ListLiveKeys func(ctx context.Context, cluster string) ([]string, error)
}

// ReconcileCluster recomputes confirmed for one consumer cluster.
func (r *UsageReconciler) ReconcileCluster(ctx context.Context, cluster string) error {
	keys, err := r.ListLiveKeys(ctx, cluster)
	if err != nil {
		return err
	}
	key := identity.UsageKey{Cluster: cluster, Group: r.Group, Resource: r.Resource, IdentityHash: r.IdentityHash}
	return r.Store.Apply(ctx, key, func(st v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus {
		return ComputeConfirmed(st, keys)
	})
}
```

Add a sweeper method in the same file:
```go
// SweepAll drops expired reservations from every QuotaUsage. Called on a ticker.
func (r *UsageReconciler) SweepAll(ctx context.Context, keys []identity.UsageKey) error {
	now := r.Store.Now
	for _, k := range keys {
		if err := r.Store.Apply(ctx, k, func(st v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus {
			return accounting.Sweep(st, now())
		}); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -race ./internal/controller/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/usage_reconciler.go internal/controller/usage_reconciler_test.go internal/kcp/vw.go
gitcs 'feat: accounting reconciler folds confirmed and sweeps reservations'
```

---

## Task 9: Webhook installer (ValidatingWebhookConfiguration for CREATE)

Installs/updates a `ValidatingWebhookConfiguration` in the provider workspace scoped to `CREATE` of the governed GVR, with the webhook path encoding the identity so the shared webhook service routes to the right `CreationValidator` (spec §4; identity-in-path resolves the admission-time identity problem).

**Files:**
- Create: `internal/controller/webhook_installer.go` — adapt `dependency-controller/internal/controller/webhook_installer.go`
- Modify: `internal/controller/consumptionquota_controller.go` — call the installer + add finalizer

**Interfaces:**
- Consumes: `v1alpha1.ConsumptionQuota` (with resolved `status.identityHash`), the dep-ctrl VW-routed client, the quota-provider `permissionClaim` for `validatingwebhookconfigurations`.
- Produces: `controller.WebhookInstaller` with `Reconcile(ctx, cq) error` and `Remove(ctx, cq) error`; webhook path template `/validate/{group}/{resource}/{identityHash}`; webhook name `consumption-quota`.

- [ ] **Step 1: Adapt the installer skeleton**

Copy `dependency-controller/internal/controller/webhook_installer.go` to `internal/controller/webhook_installer.go` and change:
- webhook name constant → `consumption-quota`
- the `rules` operation from `DELETE` → `CREATE`
- the `clientConfig` path to `/validate/<group>/<resource>/<identityHash>` (build from `cq.Spec.Governed` + `cq.Status.IdentityHash`)
- the merge key (dep-ctrl merges multiple protected GVRs per workspace) → merge by governed GVR+identity so multiple `ConsumptionQuota`s in one provider workspace produce one webhook with one rule each.

- [ ] **Step 2: Wire installer + finalizer into the ConsumptionQuota reconciler**

Modify `internal/controller/consumptionquota_controller.go`: after resolving identity and feeding the registry, call `WebhookInstaller.Reconcile(ctx, cq)`. Add a finalizer `quota.opendefense.cloud/webhook`; on deletion call `WebhookInstaller.Remove` and `Reg.Delete(ResourceRef{...})` (using the identity captured in `status.identityHash`) before clearing the finalizer.

- [ ] **Step 3: Write the failing installer test (envtest)**

Add `internal/controller/webhook_installer_test.go` (adapt dep-ctrl's equivalent): create a `ConsumptionQuota` with a resolved identity, run the installer, assert a `ValidatingWebhookConfiguration` named `consumption-quota` exists with one `CREATE` rule for the governed GVR and a clientConfig path ending `/validate/s3.example.com/buckets/<id>`.

- [ ] **Step 4: Run test to verify it fails, then passes after Step 1–2**

Run: `go test ./internal/controller/... -run Installer`
Expected: FAIL before wiring, PASS after.

- [ ] **Step 5: Commit**

```bash
git add internal/controller
gitcs 'feat: install CREATE webhook with identity-scoped path'
```

---

## Task 10: Binary wiring — `cmd/controller` and `cmd/webhook`

Assemble the two binaries using the kcp multicluster provider (adapt dep-ctrl's `cmd/*/main.go`). This is the integration glue; keep logic in the tested packages.

**Files:**
- Create: `cmd/controller/main.go` — adapt `dependency-controller/cmd/controller/main.go`
- Create: `cmd/webhook/main.go` — adapt `dependency-controller/cmd/webhook/main.go`

**Interfaces:**
- Consumes: everything above.
- Produces: two runnable binaries.

- [ ] **Step 1: Implement `cmd/controller/main.go`**

Adapt dep-ctrl's controller main:
- build an `mcmanager` over the **quota-provider** APIExport VW (watch `ConsumptionQuota`);
- register `ConsumptionQuotaReconciler{Reg, Client, WebhookInstaller}`;
- for each governed identity, start a multicluster informer over the **provider service** APIExport VW and drive `UsageReconciler.ReconcileCluster` on object add/update/delete; wire `ListLiveKeys` to that informer's cache;
- start a ticker calling `UsageReconciler.SweepAll` every `resyncInterval` (default 60s, matching the reservation TTL from spec §7.4);
- construct one shared `quota.Store{Client: <quota-ctrl workspace client>, TTL: 60s, Now: time.Now}`;
- run leader election (the reconciler must be single-writer per spec §5.2).

- [ ] **Step 2: Implement `cmd/webhook/main.go`**

Adapt dep-ctrl's webhook main:
- build the same `Registry` (populated by watching `ConsumptionQuota` via the quota-provider VW — reuse `ConsumptionQuotaReconciler` in registry-only mode, or a lighter watcher);
- construct `quota.Store` against the quota-ctrl workspace;
- mount an HTTP mux: for each path `/validate/{group}/{resource}/{identityHash}`, construct a `CreationValidator`, call `SetResource(ref)`, and serve via `admission.Webhook{Handler: v}`. Use a single catch-all handler that parses the path, looks up/creates the per-identity validator, and dispatches;
- set the readiness probe to fail until the registry's initial `ConsumptionQuota` list has synced (fail-closed on startup, spec §11);
- serve TLS using the chart-provisioned cert.

- [ ] **Step 3: Verify both binaries build**

Run: `go build ./cmd/...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd
gitcs 'feat: wire controller and webhook binaries over kcp VWs'
```

---

## Task 11: kcp manifests + Helm chart

**Files:**
- Create: `config/kcp/apiresourceschema-consumptionquotas.quota.opendefense.cloud.yaml`, `config/kcp/apiresourceschema-quotausages.quota.opendefense.cloud.yaml`, `config/kcp/apiexport-quota-provider.yaml`, `config/kcp/kustomization.yaml`
- Create: `charts/quota-controller/` (Chart.yaml, values.yaml, templates for controller + webhook Deployments, webhook Service, cert-manager Certificate, ServiceAccounts, and the bootstrap RBAC) — adapt `dependency-controller/charts/dependency-controller/`

**Interfaces:**
- Produces: deployable manifests. The quota-provider `APIExport` declares `permissionClaims: [{resource: validatingwebhookconfigurations, group: admissionregistration.k8s.io}]` (spec §10). `QuotaUsage` is **not** exported (internal CRD in the quota-ctrl workspace).

- [ ] **Step 1: Generate APIResourceSchemas from the CRDs**

Adapt dep-ctrl's flow (its `charts/.../files/apiresourceschema-*.yaml` are generated from CRDs). Produce APIResourceSchemas for `consumptionquotas` and `quotausages`.

- [ ] **Step 2: Author the quota-provider APIExport**

Create `config/kcp/apiexport-quota-provider.yaml` referencing the `ConsumptionQuota` schema and declaring the `validatingwebhookconfigurations` permissionClaim.

- [ ] **Step 3: Author the Helm chart**

Adapt dep-ctrl's chart: two Deployments (`controller`, `webhook`), webhook `Service`, cert-manager `Certificate` + self-signed `Issuer`, ServiceAccounts, and the bootstrap RBAC — including the **new** grant vs dep-ctrl: the accounting reconciler's read (`get,list,watch`) on the provider *service* `APIExport` content (spec §10). Keep dep-ctrl's per-shard/workspace-resolution RBAC bindings.

- [ ] **Step 4: Lint the chart**

Run: `helm lint charts/quota-controller`
Expected: 0 failures.

- [ ] **Step 5: Commit**

```bash
git add config charts
gitcs 'feat: kcp APIExport manifests and Helm chart'
```

---

## Task 12: Integration (envtest, run) + e2e (written, NOT run) + deploy to sysdemo

Per the Deployment & Environment section: run the envtest integration tests, **author** the e2e suite mirroring `dependency-controller/test/e2e/` but **do not run it**, then deploy to the `sysdemo` kind cluster + `root:cloud-api-system` workspace and smoke-test by hand.

**Files:**
- Create: `internal/controller/integration_test.go` (envtest: installer + reconcile loop)
- Create: `test/e2e/suite_test.go`, `test/e2e/quota_test.go` — adapt `dependency-controller/test/e2e/` (written, not executed)
- Create: `test/fixtures/*.yaml` (APIResourceSchema/APIExport/APIBinding/ConsumptionQuota/consumer resources) — adapt dep-ctrl fixtures

**Interfaces:**
- Consumes: the whole system.
- Produces: green integration suite; an authored (unrun) e2e suite; a deployed, smoke-tested controller on sysdemo.

- [ ] **Step 1: Integration test — reconcile keeps confirmed correct (envtest, run)**

In `internal/controller/integration_test.go`: start the `UsageReconciler` against a fake `ListLiveKeys`; create N `QuotaUsage`s via the `Store`; assert `confirmed` converges to the observed key count and reservations fold. Run: `go test ./internal/controller/...` → PASS.

- [ ] **Step 2: Author the e2e suite (mirror dep-ctrl) — DO NOT RUN**

Create `test/e2e/suite_test.go` + `test/e2e/quota_test.go` closely mirroring `dependency-controller/test/e2e/suite_test.go` and `dependency_test.go` (same Ginkgo bootstrap, kind+kcp setup helpers, fixture-apply utilities), covering: (a) happy path + strict boundary (3 ok / 4th denied / delete frees a slot for `defaultLimit: 3`); (b) no overshoot (10 concurrent creates vs `defaultLimit: 5` → exactly 5); (c) fail-closed (webhook scaled to 0 ⇒ create rejected ⇒ scaled up ⇒ resumes). Guard the suite behind the same build tag/skip env dep-ctrl uses so `task test` does not execute it. **Do not run these in this iteration.**

- [ ] **Step 3: Build and load images into `sysdemo`**

Run:
```bash
docker build -t quota-controller:dev -f Dockerfile .
kind load docker-image quota-controller:dev --name sysdemo
```
Expected: image loaded into the `sysdemo` nodes. (Add a `Dockerfile` adapted from `dependency-controller/Dockerfile` if not already created in Task 1/10.)

- [ ] **Step 4: Bootstrap the quota-ctrl workspace in kcp**

Run (using the sysdemo kcp kubeconfig):
```bash
export KUBECONFIG=/Users/cyrill/Documents/Repos/opendefense-gl/cat/demo/flux-sysdemo-26.7/.secrets/kcp-admin-fp-sysdemo.kubeconfig
kubectl ws root:cloud-api-system
kubectl apply -f config/kcp/            # APIResourceSchemas + quota-provider APIExport + QuotaUsage CRD
```
Expected: `APIExport/quota-provider` present with a populated `status.identityHash`.

- [ ] **Step 5: Install the Helm chart onto `sysdemo`**

Run:
```bash
helm upgrade --install quota-controller charts/quota-controller \
  --kube-context kind-sysdemo \
  --namespace quota-system --create-namespace \
  --set image.repository=quota-controller --set image.tag=dev \
  --set kcp.workspace=root:cloud-api-system
```
Expected: `controller` and `webhook` Deployments become Ready. Follow `dependency-controller`'s chart values for the kcp kubeconfig secret + cert-manager wiring.

- [ ] **Step 6: Smoke test by hand against sysdemo**

Using a provider workspace with a service export + `ConsumptionQuota{defaultLimit: 3}` and a consumer workspace bound to it (reuse dep-ctrl's demo topology under `root:cloud-api-system`): create 3 governed objects (succeed), the 4th (denied with the quota message), delete 1, create 1 (succeeds). Capture the controller/webhook logs. Report the observed outputs — do not claim success without them.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/integration_test.go test Dockerfile
gitcs 'test: integration + authored e2e; deploy manifests for sysdemo'
```

---

## Self-Review (completed against the spec)

**Spec coverage (Phase 1 scope):**
- Strict no-overshoot (R1) → Task 3 (pure reserve) + Task 4 (CAS store, concurrency test) + Task 12 Step 3. ✓
- Cap admitted objects (R2) → Task 7 (CREATE admission denies). ✓
- Adversarial/external authority (R3) → `QuotaUsage` + registry live in quota-ctrl/provider workspaces; webhook installed in provider WS (Task 9). ✓
- Default limit per resource (R4, Phase-1 portion) → Task 5 registry + Task 6. ✓
- Per-workspace scope (R5) → `UsageKey.Cluster` keying (Task 3). ✓
- Fail-closed (R6) → Task 7 (error ⇒ Errored) + Task 12 Step 4 + `failurePolicy: Fail` (Task 9/11). ✓
- Provider authorship (R7) → `ConsumptionQuota` in provider WS (Task 2/11). ✓
- Identity keying (spec §6) → Task 2/3/5/6/9. ✓
- Accounting reconciler + fold/sweep + TTL (spec §7.2–7.4) → Task 8 + Task 10 Step 1. ✓
- RBAC incl. new provider-service-VW read grant (spec §10) → Task 11 Step 3. ✓
- **Deferred to Phase 2 (correctly out of scope):** self-service `QuotaClaim`/`QuotaGrant`, second APIExport, `maximalPermissionPolicy`, auto-approve ceiling, per-consumer grants. `Registry.LimitFor` already carries the `cluster` param so grants slot in without a signature change.

**Placeholder scan:** no TBD/TODO; the two "adapt dep-ctrl" glue tasks (8/9/10/11) name the exact source file and the exact change. The one explicit implementer note (registry cleanup finalizer, Task 6 Step 5) is spelled out, not deferred.

**Type consistency:** `UsageKey{Cluster,Group,Resource,IdentityHash}`, `ResourceRef{Group,Resource,IdentityHash}`, `QuotaUsageStatus{Confirmed,Reservations}`, `Reservation{Key,ExpiresAt}`, `Store.Reserve/Apply`, `Registry.Set/Delete/LimitFor/ByGroupResource`, `accounting.Used/Reserve/Sweep/FoldConfirmed` are used identically across tasks.

## Known follow-ups (not Phase 1)
- Workspace-nesting limitation inherited from dep-ctrl's resolver (spec §14.6).
- Provider-service-VW watch scale at 1000 workspaces — load-test (spec §14, scaling discussion).
- Phase 2 plan: self-service request/approval (ADR-002).
