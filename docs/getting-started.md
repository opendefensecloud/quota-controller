# Getting started with the quota controller

This guide covers the self-service quota request/approval workflows (Iteration 1b). Iteration 1a enforcement is a prerequisite for self-service; see the [design spec](superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) for details.

## The three objects

One object per party drives the whole workflow:

- **`ConsumptionQuota`** — the **policy**. The provider defines the default limit and an optional `autoApproveCeiling` for a governed resource.
- **`QuotaClaim`** — the consumer's **view + request**. One per consumer per resource; the consumer reads their limit and edits `spec.requestedLimit` to ask for more (or less).
- **`QuotaGrant`** — the provider's **decision**. One per consumer per resource, in the provider workspace; the provider approves/rejects here, and auto-approval writes it.

The **effective limit** is what the webhook actually enforces right now.

## Prerequisites

- A kcp cluster with the quota-controller deployed
- The provider has set up a `ConsumptionQuota` for a governed resource (e.g. `buckets`)
- Consumer and provider workspaces bound to the provider's service export

## Self-service quota (consumer)

### Bind the quota-consumer export

Consumers must bind the `quota-consumer` export to see their quota claims and request higher limits.
The export lives in the quota-ctrl workspace (`root:cloud-api-system`).

```bash
# List your workspace
kubectl get workspaces

# Switch to your consumer workspace
kubectl ws <consumer-workspace>

# Create an APIBinding to the quota-consumer export (no permissionClaims to accept)
kubectl apply -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: quota-consumer
spec:
  reference:
    export:
      path: root:cloud-api-system
      name: quota-consumer
EOF
```

### View your quota claims

Once bound, the controller pre-creates a `QuotaClaim` for each governed resource type you can
access (see [ADR-005](../architecture/ADR-005-claim-discovery-via-service-export-vw.md) — the
quota-consumer export only projects claims the controller has already written; consumers cannot
create them). Use `kubectl get quotaclaims` to list them:

**Note:** Claims are pre-created by the controller within about one `--resync-interval` (default 60s) of binding the provider's service export. If the list is empty at first, wait and retry.

```bash
kubectl get quotaclaims
```

Output example:
```
NAME                       AGE
qc-buckets-abcdef01        2m
```

To view the claim's status (phase, effective limit, granted limit, reason), use:

```bash
kubectl get quotaclaim qc-buckets-abcdef01 -o jsonpath='{.status}'
```

Example `status` output:
```json
{"phase":"Approved","effectiveLimit":3,"grantedLimit":3,"reason":"auto-approved (≤ ceiling)","lastTransitionTime":"2026-07-01T09:00:00Z"}
```

The claim name format is `qc-<resource-trunc20>-<identityHash-trunc8>`. The `status` fields are
read-only to consumers (enforced by `maximalPermissionPolicy` on the export):

- **phase** — `None` | `Pending` | `Approved` | `Rejected`
- **effectiveLimit** — the limit the webhook actually enforces right now
- **grantedLimit** — the provider's approved override (if any)
- **reason** — explanation (e.g. "auto-approved (≤ ceiling)", "provider decision")

### Request a higher limit

Edit the claim's `spec.requestedLimit` to ask for a new limit:

```bash
kubectl patch quotaclaim qc-buckets-abcdef01 \
  -p '{"spec":{"requestedLimit":8}}' \
  --type=merge
```

The controller reads your request and creates or updates a `QuotaGrant` in the provider's workspace:

- If `requestedLimit ≤ provider's autoApproveCeiling` → auto-approved immediately
- If `requestedLimit > ceiling` (or no ceiling set) → routes to manual approval (`Pending`)

Watch the claim's status for the decision:

```bash
kubectl get quotaclaim qc-buckets-abcdef01 -w
```

When the provider approves (or auto-approval triggers), `phase` becomes `Approved`, `grantedLimit`
is set to the approved value, and `effectiveLimit` updates to match. Your next `CREATE` of the
resource type will be allowed up to the new limit (or denied if `Rejected`).

#### Changing your request after a decision

`spec.requestedLimit` is a live lever — editing it re-evaluates the request against the provider's
`autoApproveCeiling`:

- **Raise above what's already granted** (over the ceiling): the claim shows `phase: Pending` — an
  outstanding raise the provider must act on — while your `effectiveLimit` stays at the limit you
  already hold. You never lose your current cap by asking for more.
- **Lower below what's granted** (self-service down-scale): your `effectiveLimit` follows the
  request down — `grantedLimit` is reduced to the new value with no provider action, since a
  reduction never exceeds what was approved. Raising it back above the reduced grant is treated as a
  new raise and needs approval again.
- **After a `Rejected` decision:** changing `requestedLimit` re-opens the request. A new value at or
  below the ceiling auto-approves; above the ceiling it returns to `Pending` for a fresh provider
  decision. Re-submitting the **same** value the provider already rejected stays `Rejected` (you
  cannot re-spam an identical denied request — change the number to re-open it).

## Self-service quota (provider)

### View pending & approved grants

Grants live in the **provider workspace** alongside your `ConsumptionQuota`. Switch to the
provider workspace and list grants:

```bash
kubectl ws <provider-workspace>
kubectl get quotagrants
```

Output example:
```
NAME                                   AGE
qg-buckets-abcdef01-046029657f6cc846   2m
qg-buckets-9f8e7d6c-1a2b3c4d5e6f7a8b   5m
```

Grant names are machine-generated following the scheme `qg-<resource>-<id>-<consumer-hash>`. Identify grants by their **consumer logical
cluster** in `spec.consumer` and the **governed resource** in `spec.governed`:

```bash
kubectl get quotagrant qg-buckets-abcdef01-046029657f6cc846 -o yaml
```

Example output (a grant with a pending raise — approved at 6, consumer now asking for 10):
```yaml
spec:
  consumer: 1e2d5ot5grlnw1x0
  governedRef: bucket-quota          # names the ConsumptionQuota this grant overrides
  governed:                          # the governed resource's kcp identity tuple
    group: s3.example.com
    resource: buckets
    identityHash: a1b2c3…
  requestedLimit: 10                 # what the consumer is asking for
  grantedLimit: 6                    # what you approved (what the webhook enforces)
  decision: Approved                 # your standing verdict
  reason: "approved at 6; escalation to 10 awaits provider action"
status:
  phase: Pending                     # what needs YOUR action now
```

### Approve or reject a request

A grant needs your attention when its **`status.phase` is `Pending`**. Review `spec.consumer` and
`spec.requestedLimit`, then handle whichever case applies:

- **First-time request** (`spec.decision: Pending`) — approve by setting **both** `decision: Approved`
  and `grantedLimit`, or reject with a reason. (An `Approved` grant with no `grantedLimit` stays
  `Pending` — nothing was actually granted.)
- **A raise on an already-approved grant** (`spec.decision` stays `Approved`, `requestedLimit >
  grantedLimit`) — the consumer keeps the limit you already granted (the webhook never drops it) and
  the raise waits for you. To grant it, bump `spec.grantedLimit`; to hold the line, leave it and
  `status.phase` stays `Pending` as a standing reminder. `spec.reason` explains the pending raise.

> `spec.decision` is your standing verdict **and** the enforcement switch (the webhook enforces only
> while it's `Approved`); `status.phase` is the derived "needs action" state, mirrored on the
> consumer's claim. They differ during a raise — that's expected, not a bug.

```bash
# Approve
kubectl patch quotagrant <grant-name> \
  -p '{"spec":{"decision":"Approved","grantedLimit":8}}' \
  --type=merge

# Or reject
kubectl patch quotagrant <grant-name> \
  -p '{"spec":{"decision":"Rejected","reason":"exceeds team budget"}}' \
  --type=merge
```

Once decided, the controller writes the decision back to the consumer's claim:

- Approved → consumer's claim `phase: Approved`, `grantedLimit` set, `effectiveLimit` updates
- Rejected → consumer's claim `phase: Rejected`, the enforcement webhook continues using `defaultLimit`

### Auto-approval ceiling

To skip manual approval for requests below a limit, set `autoApproveCeiling` on your
`ConsumptionQuota`:

```bash
kubectl patch consumptionquota bucket-quota \
  -p '{"spec":{"autoApproveCeiling":10}}' \
  --type=merge
```

Consumer requests ≤ 10 are now auto-approved without needing provider action. Requests above 10
still go `Pending`.

## Portal visibility (Platform Mesh)

On Platform Mesh installations, apply the optional UI add-on in the quota-ctrl
workspace (`root:cloud-api-system`):

```bash
kubectl apply -k config/platform-mesh/
```

Workspaces then see quota state in the portal navigation under a "Quota"
category, with direction-encoding names (a workspace can be provider and
consumer at once):

- **My quotas** (consumer, needs the `quota-consumer` binding): your claims —
  effective limit, request phase, reason.
- **Quota policies** (provider, needs the `quota-provider` binding): the
  ConsumptionQuotas you define.
- **Quota approvals** (provider): your consumers' QuotaGrants — approve or
  reject pending requests.

To give every new account the `quota-consumer` binding automatically (instead of
binding each workspace by hand), add it to `PlatformMesh.spec.kcp.extraDefaultAPIBindings`
— see [`config/platform-mesh/README.md`](../config/platform-mesh/README.md).

Consumers request raises and providers approve/reject directly in the portal's detail-view Edit
dialog — the same `spec.requestedLimit` / `spec.decision` flow as the CLI sections above.

> **Note — setting a numeric limit.** `spec.grantedLimit` is deliberately *not* editable in the
> portal: the generic Edit form can't submit an unset number (GraphQL's `Int` type rejects `""`), so
> nullable numeric fields are display-only and set via `kubectl patch`. A provider who approves in
> the portal therefore sets only `decision`; until they also set `grantedLimit` via CLI, the policy
> default stays enforced and the claim reads `phase: Pending` (*"approved but spec.grantedLimit is
> not set …"*) rather than a misleading `Approved`.

### Authorization notes

The add-on's `core.platform-mesh.io` binding accepts the platform's full permission-claim set (see
the commented [`apibinding-core-platform-mesh.yaml`](../config/platform-mesh/apibinding-core-platform-mesh.yaml)).
This is required for portal **authorization**, not just for hosting `ContentConfiguration`s: the
platform's `security-operator-generator` reads this workspace's APIExports to build the OpenFGA
model for each quota type, and the ReBAC webhook reads `core.kcp.io/logicalclusters` through it for
every consumer. With a partial set, real portal users get 403s on quota views. (Grants become
visible in the portal once a claim exists and a raise is requested.)

> **Provider-side limitation.** The generated authorization model needs an `AccountInfo` in the
> workspace. Provider workspaces the platform hasn't onboarded as an account (e.g. `root:providers:s3`)
> have none, so **"Quota policies" and "Quota approvals" stay portal-unauthorized** there until
> they're onboarded. The CLI works regardless — provider-side access is plain kcp RBAC, independent
> of the portal's ReBAC layer.

## API reference

See the [design spec](superpowers/specs/2026-06-29-kcp-consumption-quota-design.md) for the full
schema and [ADR-002](../architecture/ADR-002-self-service-quota-requests.md) for the request/approval
workflow rationale. The claim-discovery mechanism is documented in
[ADR-005](../architecture/ADR-005-claim-discovery-via-service-export-vw.md).
