// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// accountingManager owns the per-governed-export multicluster informers that
// keep each QuotaUsage's confirmed count in sync with the live governed objects.
//
// For every distinct governed identity a ConsumptionQuota names, it stands up a
// dedicated multicluster-runtime manager over that provider's *service* APIExport
// virtual workspace, watching the governed GVR across all consumer clusters.
// On any object add/update/delete it recomputes confirmed for the affected
// consumer cluster (UsageReconciler.ReconcileCluster), and a ticker sweeps
// expired reservations every resync interval (UsageReconciler.SweepAll).
//
// The set is reference-counted by the ConsumptionQuotas that reference it, so it
// is torn down only when the last referencing quota is deleted. Because the
// controller's ConsumptionQuota reconciler is leader-election gated, Ensure/Remove
// only run on the elected leader, satisfying the single-writer requirement.
type accountingManager struct {
	rootCtx context.Context //nolint:containedctx // long-lived process context for sub-manager lifetimes
	baseCfg *rest.Config
	scheme  *runtime.Scheme
	store   *quota.Store
	resync  time.Duration
	log     logr.Logger

	// ensurer and policies wire the ADR-005 claim discovery Runnable into each
	// sub-manager (see start()). Left nil by the 1a-only unit tests in
	// accounting_test.go, which skips discovery entirely so those lifecycle
	// specs stay valid without a self-service stack.
	ensurer  *controller.ClaimEnsurer
	policies *selfservice.PolicyIndex

	// startFn launches the sub-manager for one governed export and must
	// register sets[key] before a successful return (as start does). It exists
	// as a field so lifecycle tests can exercise Ensure/Remove/retryStart
	// without standing up a real multicluster manager.
	startFn func(ctx context.Context, providerCluster string, g governed, key string) (*accountingSet, error)
	// retryBackoff paces retryStart's relaunch attempts after an unexpected
	// self-stop; tests shrink it.
	retryBackoff wait.Backoff

	mu       sync.Mutex
	sets     map[string]*accountingSet // key -> running set
	refs     map[string]sets.Set[string]
	starting sets.Set[string] // keys with an in-flight start
}

// accountingSet is a single running per-governed-export accounting manager.
type accountingSet struct {
	cancel context.CancelFunc
}

func newAccountingManager(rootCtx context.Context, baseCfg *rest.Config, scheme *runtime.Scheme, store *quota.Store, resync time.Duration, log logr.Logger) *accountingManager {
	a := &accountingManager{
		rootCtx: rootCtx,
		baseCfg: baseCfg,
		scheme:  scheme,
		store:   store,
		resync:  resync,
		log:     log,
		// Capped exponential backoff (5 s → 10 s → … → 2 m) for relaunching a
		// self-stopped set. Steps is effectively unbounded; Duration is capped
		// at 2 m after a few doublings.
		retryBackoff: wait.Backoff{
			Duration: 5 * time.Second,
			Factor:   2.0,
			Cap:      2 * time.Minute,
			Steps:    1000,
		},
		sets:     map[string]*accountingSet{},
		refs:     map[string]sets.Set[string]{},
		starting: sets.New[string](),
	}
	a.startFn = a.start

	return a
}

// accountingKey uniquely identifies one accounting informer set.
func accountingKey(group, resource, identityHash string) string {
	return group + "/" + resource + "/" + identityHash
}

// Ensure guarantees that an accounting informer set is running for the governed
// export named by cq (which must have a resolved status.identityHash). It is
// idempotent and reference-counts the referencing ConsumptionQuota.
func (a *accountingManager) Ensure(ctx context.Context, providerCluster string, cq *v1alpha1.ConsumptionQuota) error {
	group := cq.Spec.Governed.Group
	version := cq.Spec.Governed.Version
	resource := cq.Spec.Governed.Resource
	id := cq.Status.IdentityHash
	apiExportName := cq.Spec.Governed.APIExportName
	key := accountingKey(group, resource, id)
	cqRef := providerCluster + "/" + cq.Name

	a.mu.Lock()
	// A ConsumptionQuota references exactly one governed export; if this CQ was
	// previously counted under a different key (e.g. its spec changed), drop the
	// stale reference so the old set can be reclaimed.
	a.dropRefLocked(cqRef, key)
	if a.refs[key] == nil {
		a.refs[key] = sets.New[string]()
	}
	a.refs[key].Insert(cqRef)

	if _, running := a.sets[key]; running || a.starting.Has(key) {
		a.mu.Unlock()
		return nil
	}
	a.starting.Insert(key)
	a.mu.Unlock()

	_, err := a.startFn(ctx, providerCluster, governed{group, version, resource, id, apiExportName}, key)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.starting.Delete(key)
	if err != nil {
		// Roll back the reference so a later reconcile retries the start.
		// start() only registers sets[key] on the success path (before spawn),
		// so there is nothing to unregister here.
		a.dropRefLocked(cqRef, "")
		return err
	}

	return nil
}

// Remove drops cq's reference to its accounting set and tears the set down once
// no ConsumptionQuota references it anymore.
func (a *accountingManager) Remove(providerCluster, cqName string) {
	cqRef := providerCluster + "/" + cqName
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropRefLocked(cqRef, "")
}

// dropRefLocked removes cqRef from every accounting key except keepKey and tears
// down any set left with no references. Callers must hold a.mu.
func (a *accountingManager) dropRefLocked(cqRef, keepKey string) {
	for key, refs := range a.refs {
		if key == keepKey || !refs.Has(cqRef) {
			continue
		}
		refs.Delete(cqRef)
		if refs.Len() > 0 {
			continue
		}
		delete(a.refs, key)
		if set, ok := a.sets[key]; ok {
			set.cancel()
			delete(a.sets, key)
			a.log.Info("stopped accounting manager", "key", key)
		}
	}
}

// governed bundles the identifiers of one governed export.
type governed struct {
	group, version, resource, identityHash, apiExportName string
}

// governedControllerName derives a stable, per-governed-export controller name.
//
// controller-runtime keeps controller names in a process-global set to catch
// duplicate metrics registration. Even with SkipNameValidation set on the
// ephemeral sub-managers, a distinct name per governed export keeps controller
// logs unambiguous when several accounting sets run side by side. The identity
// hash disambiguates two exports that share a resource name across groups.
func governedControllerName(g governed) string {
	id := g.identityHash
	if len(id) > 8 {
		id = id[:8]
	}

	return fmt.Sprintf("governed-usage-%s-%s", g.resource, id)
}

// start builds and launches a dedicated multicluster manager watching the
// governed GVR across all consumer clusters of the service APIExport's VW. The
// sub-manager's lifetime is deliberately bound to the process root context
// (a.rootCtx), not the per-reconcile ctx which is cancelled as soon as this
// reconcile returns.
//
//nolint:contextcheck,unparam // contextcheck: sub-manager lifetime is bound to a.rootCtx by design; unparam: *accountingSet return kept for API stability
func (a *accountingManager) start(ctx context.Context, providerCluster string, g governed, key string) (*accountingSet, error) {
	// The governed service APIExport, its APIResourceSchema and its endpoint slice
	// all live in the provider workspace and must be read through a DIRECT
	// workspace-scoped client. The quota-provider virtual workspace does not serve
	// apiexports/apiresourceschemas/apiexportendpointslices, so reading them via
	// the VW client fails ("no matches for kind APIExport").
	svcCfg := kcp.WorkspaceConfig(a.baseCfg, providerCluster)

	directClient, err := client.New(svcCfg, client.Options{Scheme: a.scheme})
	if err != nil {
		return nil, fmt.Errorf("creating direct provider-workspace client: %w", err)
	}

	kind, err := kcp.ResolveKind(ctx, directClient, g.apiExportName, g.group, g.resource)
	if err != nil {
		return nil, fmt.Errorf("resolving kind for %s/%s: %w", g.group, g.resource, err)
	}

	sliceName, err := kcp.EndpointSliceName(ctx, directClient, g.apiExportName)
	if err != nil {
		return nil, fmt.Errorf("resolving endpoint slice for %s: %w", g.apiExportName, err)
	}

	// Confirm the service export's VW is advertised before wiring the provider.
	// Captured here and reused for claim discovery below (one endpoint-slice list).
	svcVWURL, err := kcp.VirtualWorkspaceURL(ctx, directClient, g.apiExportName)
	if err != nil {
		return nil, fmt.Errorf("service export %s VW not ready: %w", g.apiExportName, err)
	}

	provider, err := apiexport.New(svcCfg, sliceName, apiexport.Options{Scheme: a.scheme})
	if err != nil {
		return nil, fmt.Errorf("creating service apiexport provider: %w", err)
	}

	// Sub-managers must not run their own metrics/health servers (port clashes)
	// and must not contend for leader election (already gated by the parent).
	//
	// SkipNameValidation: controller-runtime dedupes controller names in a
	// process-global set that is never cleaned up on manager shutdown. Each
	// governed export gets its own ephemeral sub-manager whose controller shares
	// the base name "governed-usage"; without this, a second ConsumptionQuota (or
	// any retryStart after a self-stop) fails permanently with "controller with
	// name governed-usage already exists". The global check only guards metrics
	// name collisions, which cannot happen here (metrics are disabled above).
	subMgr, err := mcmanager.New(svcCfg, provider, manager.Options{
		Scheme:                 a.scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Controller:             ctrlconfig.Controller{SkipNameValidation: new(true)},
	})
	if err != nil {
		return nil, fmt.Errorf("creating accounting manager: %w", err)
	}

	gvk := schema.GroupVersionKind{Group: g.group, Version: g.version, Kind: kind}
	listGVK := schema.GroupVersionKind{Group: g.group, Version: g.version, Kind: kind + "List"}

	ur := &controller.UsageReconciler{
		Store:          a.store,
		Group:          g.group,
		Resource:       g.resource,
		IdentityHash:   g.identityHash,
		ResyncInterval: a.resync,
		ListLiveKeys: func(ctx context.Context, clusterName string) ([]string, error) {
			cl, err := subMgr.GetCluster(ctx, multicluster.ClusterName(clusterName))
			if err != nil {
				return nil, err
			}
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(listGVK)
			if err := cl.GetCache().List(ctx, list); err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(list.Items))
			for i := range list.Items {
				keys = append(keys, quota.ObjectKey(list.Items[i].GetNamespace(), list.Items[i].GetName()))
			}

			return keys, nil
		},
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	if err := mcbuilder.ControllerManagedBy(subMgr).
		Named(governedControllerName(g)).
		For(obj).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return ctrl.Result{}, ur.ReconcileCluster(ctx, req.ClusterName.String())
		})); err != nil {
		return nil, fmt.Errorf("building governed-usage controller: %w", err)
	}

	// Reservation sweep ticker.
	if err := subMgr.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		t := time.NewTicker(a.resync)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				keys, err := a.usageKeys(ctx, g)
				if err != nil {
					a.log.Error(err, "sweep: listing usage keys", "key", key)
					continue
				}
				if err := ur.SweepAll(ctx, keys); err != nil {
					a.log.Error(err, "sweep failed", "key", key)
				}
			}
		}
	})); err != nil {
		return nil, fmt.Errorf("adding sweep runnable: %w", err)
	}

	// Claim discovery (ADR-005): skipped when the self-service stack is not
	// wired in (a.ensurer == nil), which keeps the 1a-only lifecycle specs in
	// accounting_test.go valid without a PolicyIndex/ClaimEnsurer.
	if a.ensurer != nil {
		svcVWClient, err := kcp.VWClient(a.baseCfg, svcVWURL, "*", a.scheme)
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

	set := &accountingSet{}
	subCtx, cancel := context.WithCancel(a.rootCtx)
	set.cancel = cancel

	// Register before spawning so the watcher goroutine can never observe
	// sets[key] == nil: if subMgr.Start returns immediately (fast fail), the
	// goroutine acquires a.mu and sees the correct set, preventing an early
	// return that would leave a dead manager permanently registered.
	a.mu.Lock()
	a.sets[key] = set
	a.mu.Unlock()

	go func() {
		a.log.Info("starting accounting manager", "key", key, "gvk", gvk.String(), "providerWorkspace", providerCluster)
		if err := subMgr.Start(subCtx); err != nil {
			a.log.Error(err, "accounting manager stopped with error", "key", key)
		}
		// Always cancel the sub-context. On intentional teardown dropRefLocked
		// already called cancel(); this second call is a no-op. On unexpected
		// self-stop (e.g. the service VW stopped serving the governed GVR) this is
		// the ONLY cancel call — fixes the bounded context/goroutine leak.
		cancel()

		a.mu.Lock()
		if a.sets[key] != set {
			// A concurrent Ensure or retry already installed a replacement set;
			// our instance was cleaned up externally — nothing left to do.
			a.mu.Unlock()
			return
		}
		// This manager instance is dead regardless of why it stopped.
		delete(a.sets, key)

		// Distinguish intentional teardown from unexpected self-stop:
		// dropRefLocked deletes refs[key] *before* calling cancel(), so if
		// refs[key] still has entries when Start returned, the stop was unexpected.
		liveRefs := a.refs[key].Len()
		if liveRefs == 0 {
			// Intentional teardown — dropRefLocked already cleared refs[key].
			a.mu.Unlock()
			return
		}

		// Unexpected self-stop with live references: keep refs[key] intact so
		// dropRefLocked can still reclaim them cleanly, and schedule a bounded
		// retry. Recovery must be prompt — never dependent on the ~10 h resync.
		a.log.Info("accounting manager stopped unexpectedly; scheduling bounded retry",
			"key", key, "liveRefs", liveRefs)
		a.mu.Unlock()

		go a.retryStart(g, key, providerCluster)
	}()

	return set, nil
}

// retryStart re-launches the accounting sub-manager for key after an unexpected
// self-stop, paced by a.retryBackoff. It exits once the sub-manager is
// successfully restarted, refs[key] drops to 0, or the root context is done.
// The same a.mu + starting in-flight guard used by Ensure prevents concurrent
// CQ reconciles from double-starting the same key.
func (a *accountingManager) retryStart(g governed, key, providerCluster string) {
	backoff := a.retryBackoff

	for {
		delay := backoff.Step()
		select {
		case <-a.rootCtx.Done():
			a.log.Info("root context done; aborting accounting manager retry", "key", key)
			return
		case <-time.After(delay):
		}

		// Re-check state under the lock before the expensive restart attempt.
		a.mu.Lock()
		if a.refs[key].Len() == 0 {
			// All CQ references were removed while we waited; no restart needed.
			a.mu.Unlock()
			return
		}
		if _, running := a.sets[key]; running || a.starting.Has(key) {
			// A concurrent Ensure already started a replacement; our job is done.
			a.mu.Unlock()
			return
		}
		a.starting.Insert(key)
		a.mu.Unlock()

		_, err := a.startFn(a.rootCtx, providerCluster, g, key)

		a.mu.Lock()
		a.starting.Delete(key)
		if err != nil {
			a.log.Error(err, "accounting manager restart failed; will retry", "key", key)
			a.mu.Unlock()

			continue
		}
		// start() already registered sets[key] before spawning the goroutine.
		a.mu.Unlock()
		a.log.Info("accounting manager restarted successfully", "key", key)

		return
	}
}

// usageKeys lists the QuotaUsage ledgers belonging to a governed export so the
// sweep can reclaim expired reservations across every consumer that has one.
func (a *accountingManager) usageKeys(ctx context.Context, g governed) ([]identity.UsageKey, error) {
	var list v1alpha1.QuotaUsageList
	if err := a.store.Client.List(ctx, &list); err != nil {
		return nil, err
	}

	var keys []identity.UsageKey
	for i := range list.Items {
		u := &list.Items[i]
		if u.Spec.Group == g.group && u.Spec.Resource == g.resource && u.Spec.IdentityHash == g.identityHash {
			keys = append(keys, identity.UsageKey{
				Cluster:      u.Spec.Consumer,
				Group:        g.group,
				Resource:     g.resource,
				IdentityHash: g.identityHash,
			})
		}
	}

	return keys, nil
}
