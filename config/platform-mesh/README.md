# Platform Mesh UI add-on

Optional. Makes quota state visible in the Platform Mesh portal: consumers get
"My quotas" (QuotaClaims), providers get "Quota policies" (ConsumptionQuotas)
and "Quota approvals" (QuotaGrants). Apply this kustomization in the quota-ctrl
workspace (`root:cloud-api-system`); it binds `core.platform-mesh.io` and hosts
the two ContentConfigurations routed to workspaces via their bound quota
APIExports (`ui.platform-mesh.io/content-for`). The quota system itself works
without this directory.

## Auto-binding `quota-consumer` to every account

A consumer workspace only gets the "My quotas" surface once it binds the
`quota-consumer` APIExport. To bind it automatically for every **account**-type
workspace at creation — instead of per-workspace by hand — add this entry to the
platform's `PlatformMesh` CR (`platform-mesh-system/platform-mesh`) under
`spec.kcp.extraDefaultAPIBindings`:

```yaml
- export: quota-consumer
  path: root:cloud-api-system
  workspaceTypePath: root:account
```

kro reconciles this into the `account` WorkspaceType's `defaultAPIBindings` (the
same hook the platform already uses for `dependencies.opendefense.cloud` on the
`provider` type); the `acme-account` type inherits it via `extends`. The binding
needs no permission claims.

Make this change in the platform's GitOps source — the `PlatformMesh` CR is
Flux-managed and kro owns the WorkspaceType, so a live `kubectl` patch (of the CR
or the WorkspaceType) would be reverted. It applies only to accounts created
*after* the entry lands; existing accounts are unaffected.
