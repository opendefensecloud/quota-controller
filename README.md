# quota-controller

[![Build status](https://github.com/opendefensecloud/quota-controller/actions/workflows/golang.yaml/badge.svg)](https://github.com/opendefensecloud/quota-controller/actions/workflows/golang.yaml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/opendefensecloud/quota-controller/badge)](https://scorecard.dev/viewer/?uri=github.com/opendefensecloud/quota-controller)
[![GitHub Release](https://img.shields.io/github/v/release/opendefensecloud/quota-controller)](https://github.com/opendefensecloud/quota-controller/releases/latest)


A kcp-aware admission-webhook controller that enforces consumption-based quotas across kcp
workspaces. It intercepts resource creation and updates via a ValidatingWebhookConfiguration,
checks current usage against workspace-scoped quota policies, and denies requests that would
exceed the allowed limits. See [`docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md`](docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md)
for the full design and [`architecture/`](architecture/) for ADRs.

## Status / Roadmap

The [design spec](docs/superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) splits the
work into iterations. **Iteration 1a (enforcement) and Iteration 1b (self-service) are both implemented.**
Iteration 2 (aggregate quotas) is designed-for only.

| Stage | Scope | Status |
| --- | --- | --- |
| **Iteration 1a — count enforcement** | Provider-set `defaultLimit`; strict, no-overshoot CAS-reserved admission webhook; per-workspace count accounting ([ADR-001](architecture/ADR-001-external-cas-quota-enforcement.md)) | ✅ **Implemented** — deployed & validated on `sysdemo` |
| **Iteration 1b — self-service** | Consumer request/approval workflow: `QuotaGrant`, `QuotaClaim`, `autoApproveCeiling`, the consumer-facing `APIExport` ([ADR-002](architecture/ADR-002-self-service-quota-requests.md)); claim discovery via service export VW ([ADR-005](architecture/ADR-005-claim-discovery-via-service-export-vw.md)) | ✅ **Implemented** — see [getting-started](docs/getting-started.md) for self-service workflows |
| **Iteration 2 — aggregate/property quotas** | Cap by an aggregate property (e.g. total size < 1 TiB), `by: Sum` | 📐 **Designed-for, not built** |

> **Naming note.** The enforcement [plan](docs/superpowers/plans/2026-07-01-consumption-quota-phase1-enforcement.md)
> calls these Phase 1 / Phase 2; this README's Iteration 1a / 1b / 2 vocabulary (from the design
> spec) is canonical.

## Getting started

### Prerequisites

Install [Nix](https://nixos.org/download/) and [direnv](https://direnv.net/) if not already present.

### Enter the dev shell

```bash
direnv allow   # automatically activates on every subsequent cd
# — or —
nix develop
```

This puts `go`, `gopls`, `golangci-lint`, `task`, `controller-gen`, `setup-envtest`, `kind`,
`kubectl`, and `helm` on your `$PATH`.

### Run tests

```bash
task test
```

The integration suites run against [envtest](https://book.kubebuilder.io/reference/envtest.html).
`task test` resolves the required binaries through `setup-envtest`, downloading and
caching them on first run (network access needed once). Override the Kubernetes
version with `ENVTEST_K8S_VERSION=x.y.z task test`.

### Run the linter

```bash
task lint
```

### Run locally

```bash
task run   # requires KUBECONFIG pointing at a kcp quota-ctrl workspace
```

### Using the controller

For the self-service quota workflows — requesting a higher limit, approving/rejecting, and portal
visibility — see the [usage guide](docs/getting-started.md).

## Project layout

```
api/          CRD types and deepcopy-generated code
cmd/
  controller/ main entrypoint
config/
  crds/       generated CustomResourceDefinitions
  rbac/       ClusterRole / ClusterRoleBinding manifests
  webhook/    ValidatingWebhookConfiguration
internal/     internal packages (reconcilers, webhook handlers, …)
test/         integration tests (envtest + Ginkgo/Gomega)
architecture/ Architecture Decision Records
docs/         Usage guide (getting-started.md), design specs, planning notes
```
