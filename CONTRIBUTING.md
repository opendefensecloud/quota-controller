# Contributing

Thanks for contributing. This repository provides a kcp-aware controller and
admission webhook that enforce consumption quotas across workspaces.

## Start with an assigned issue

**Every pull request needs an issue, and every issue needs an assignee before
work starts.** This is not bureaucracy: design discussion belongs in the issue,
where it costs a comment to change direction rather than a rewrite.

1. Open an issue, or find an open one, and wait for a maintainer to triage it.
2. Get it assigned. Maintainers assign themselves. GitHub does not let people
   outside the org self-assign, so comment on the issue and a maintainer will
   assign you.
3. Work only on issues assigned to you. If an assigned issue has been quiet for
   a couple of weeks, ask in the thread before picking it up.
4. Link the issue from the pull request body (`Closes #123`).

A PR without a linked issue, or from someone other than the assignee, gets
closed with a pointer back here. Three cases are exempt:

| Exempt | Why |
| --- | --- |
| Renovate and other bot PRs | Opened by automation and tracked on the Renovate dependency dashboard |
| Typos and docs-only fixes | Nothing to design — the diff is the discussion |
| Security fixes | These start privately, never in a public issue. See [Security](#security) |

## Architectural decisions get an ADR

Quota enforcement decisions are load-bearing and easy to forget the reasons
for — fail-closed behaviour, which client reads a governed APIExport, how
external CAS quotas are counted. If your change makes or reverses a decision of
that kind, add an ADR under [`architecture/`](architecture) alongside the code:
`ADR-XXX-<slug>.md`, zero-padded, continuing from the highest existing number,
covering the context, the decision, and the consequences.

## Signed commits are required

`main` is protected with `required_signatures`. **An unsigned commit cannot be
merged.** The rule is applied by `make repo-settings` from
[dev-kit](https://github.com/opendefensecloud/dev-kit).

SSH signing is the least friction if you already push over SSH:

```bash
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
```

Then add the same key to GitHub as a **signing key** (Settings → SSH and GPG
keys → New SSH key → type *Signing Key*). A key added only as an authentication
key will sign locally but show as *Unverified* on GitHub.

For GPG instead, see
[GitHub's guide](https://docs.github.com/en/authentication/managing-commit-signature-verification).

Check before pushing — `git log --show-signature -1`, or:

```bash
git log -5 --format='%h %G? %s'   # G = good signature, N = unsigned
```

## Sign off your commits (DCO)

Certify that you wrote the change and may submit it under the project's license,
per the [Developer Certificate of Origin](https://developercertificate.org/):

```bash
git commit -s -m "fix: recompute usage when a ConsumptionQuota is renamed"
```

`-s` appends a `Signed-off-by:` trailer with your `user.name` and `user.email`.
Use a real name and a reachable address.

> Not enforced in CI today, so nothing fails if you forget. Sign off anyway —
> retrofitting it across a history means rewriting commits.

There is no config that adds the trailer to every `git commit` (`format.signOff`
only affects `git format-patch`). Use an alias instead:

```bash
git config --global alias.cs 'commit -s'
```

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/), enforced twice:
`commitlint` checks every commit in the PR against
[`.commitlintrc.yml`](.commitlintrc.yml), and a separate check validates the PR
title. Allowed types and examples are documented once, in
[dev-kit's CONTRIBUTING](https://github.com/opendefensecloud/dev-kit/blob/main/docs/CONTRIBUTING.md).

The PR title is linted because squash-merging is enabled here: on a squash it
becomes the commit subject on `main`.

Scope with the component where it applies:

```
feat(webhook): reject creates that would exceed a bound quota
fix(controller): recompute usage after a ConsumptionQuota rename
ci: pin the zizmor action to a commit sha
```

## Before you open a PR

`direnv allow` (or `nix develop`) gives you a shell with Go, `golangci-lint`,
`task`, `controller-gen`, `setup-envtest`, `kind`, `kubectl`, and `helm` on
`$PATH`. Entering it also installs the git hooks, which run `make fmt`,
`make lint`, and — when `go.mod` or `go.sum` changes — an OSV scan.

```bash
task lint            # license headers plus golangci-lint
task test            # unit and integration tests against envtest
make test-e2e        # e2e tests, build-tagged; needs kind, kcp and helm
```

`task` targets delegate to the Makefile, which is the canonical entry point —
`make help` lists everything, including targets Task does not wrap. `task test`
resolves envtest binaries through `setup-envtest` and downloads them on first
run; override the Kubernetes version with `ENVTEST_K8S_VERSION=x.y.z task test`.

## What a pull request runs

| Check | Runs on | Does |
| --- | --- | --- |
| OSV-Scanner | every PR | Scans the dependency tree. **The only required status check** — nothing merges until it is green |
| Conventional Commits | every PR | `commitlint` over every commit, plus the PR title |
| Zizmor | every PR | Static analysis of the workflows — unpinned actions, template injection, over-broad permissions, credential persistence |
| Golang CI | Go sources, `go.mod`, `go.sum`, `Makefile`, `flake.nix` | `golangci-lint`, license headers, `go build`, `go vet`, `make test` |
| Helm Lint | `charts/**` | `helm lint` and `helm template` over each chart |
| Docker build | `ok-to-image` label | Builds the `quota-controller` and `quota-webhook` images. A maintainer adds the label |
| Helm publish | `ok-to-helm` label | Packages the chart. A maintainer adds the label |

Path filters mean a docs-only PR skips Golang CI and Helm Lint. Only
OSV-Scanner blocks the merge today; the rest are advisory, so read them rather
than merging past a red one. Fuzzing runs weekly on a schedule, not on PRs —
`workflow_dispatch` triggers it by hand.

## Getting merged

Beyond green checks, the `main` ruleset requires:

- one approving review from the maintainer team
- every review conversation resolved
- the branch up to date with `main`
- approval of the most recent push — pushing new commits dismisses earlier
  approvals, so re-request review after a fixup

Publishing happens after the merge, not on the PR: pushes to `main` build the
container images, and a published GitHub release (or a `v*` tag) publishes both
the images and the Helm chart.

## Security

Do not open a public issue for a vulnerability. Report it through the private
advisory form linked from [SECURITY.md](SECURITY.md), and expect coordinated
disclosure.

Reports must show that the finding is reachable in this controller. Raw
dependency-scanner output, without evidence the CVE can be used against the
quota-controller, is not accepted.
