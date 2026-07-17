// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

const (
	// defaultVWReadyTimeout is the default bound on how long PopulateRegistry
	// waits for the quota-provider APIExport's virtual workspace to become
	// listable (overridable via --vw-ready-timeout for slow kcp cold-starts).
	// kcp populates the backing APIExportEndpointSlice asynchronously after
	// startup, so a transient "no endpoints yet" must be retried rather than
	// crash-looping the process. The window is generous enough to absorb normal
	// startup races while still surfacing a genuine misconfiguration as a
	// terminal error.
	defaultVWReadyTimeout = 5 * time.Minute
	vwReadyInterval       = 3 * time.Second
)

// registryWatcher keeps the webhook's limit registry in sync with the
// ConsumptionQuota objects across provider workspaces. It is a read-only mirror:
// unlike the controller's reconciler it never writes status or finalizers, since
// only the controller owns those.
type registryWatcher struct {
	mgr           mcmanager.Manager
	scheme        *runtime.Scheme
	apiExportName string
	reg           *registry.Registry
	// vwReadyTimeout bounds PopulateRegistry's wait for a listable virtual
	// workspace; zero means defaultVWReadyTimeout.
	vwReadyTimeout time.Duration

	mu    sync.Mutex
	known map[string]registry.ResourceRef // cqRef (cluster/name) -> registry entry
}

// Reconcile mirrors a single ConsumptionQuota into the registry. Quotas whose
// identityHash has not yet been stamped by the controller are skipped; a later
// event fires once the controller resolves it.
func (w *registryWatcher) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cq", req.Name, "cluster", req.ClusterName)

	cl, err := w.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	cqRef := req.ClusterName.String() + "/" + req.Name

	cq := &v1alpha1.ConsumptionQuota{}
	if err := cl.GetClient().Get(ctx, req.NamespacedName, cq); err != nil {
		if client.IgnoreNotFound(err) == nil {
			w.forget(cqRef)
			logger.Info("ConsumptionQuota gone, removed from registry")

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	if !cq.DeletionTimestamp.IsZero() {
		w.forget(cqRef)

		return ctrl.Result{}, nil
	}

	if cq.Status.IdentityHash == "" {
		// Identity not resolved yet; wait for the controller to stamp it.
		return ctrl.Result{}, nil
	}

	w.remember(cqRef, cq)

	return ctrl.Result{}, nil
}

// remember records the quota's limit in the registry (and its reverse mapping so
// a later deletion can be removed precisely).
func (w *registryWatcher) remember(cqRef string, cq *v1alpha1.ConsumptionQuota) {
	ref := registry.ResourceRef{
		Group:        cq.Spec.Governed.Group,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: cq.Status.IdentityHash,
	}

	w.mu.Lock()
	w.known[cqRef] = ref
	w.mu.Unlock()

	w.reg.Set(ref, cq.Spec.DefaultLimit)
}

// forget removes a quota's registry entry.
func (w *registryWatcher) forget(cqRef string) {
	w.mu.Lock()
	ref, ok := w.known[cqRef]
	delete(w.known, cqRef)
	w.mu.Unlock()

	if ok {
		w.reg.Delete(ref)
	}
}

// PopulateRegistry lists every ConsumptionQuota AND QuotaGrant from each
// shard's quota-provider virtual workspace and seeds the registry (default
// limits from ConsumptionQuotas, per-consumer overrides from Approved
// QuotaGrants). It runs once, after the manager starts, and gates the
// readiness probe.
func (w *registryWatcher) PopulateRegistry(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("registry-watcher")

	var (
		total   int
		shards  int
		lastErr error
	)

	timeout := w.vwReadyTimeout
	if timeout <= 0 {
		timeout = defaultVWReadyTimeout
	}

	// The virtual workspace (and the APIBinding it serves) come up asynchronously
	// after the manager starts. Poll until the initial list succeeds rather than
	// failing the runnable, which would crash-loop the process on a startup-timing
	// transient. The readyz gate keeps the webhook fail-closed throughout the wait.
	waitErr := wait.PollUntilContextTimeout(ctx, vwReadyInterval, timeout, true, func(ctx context.Context) (bool, error) {
		vwClients, err := w.virtualWorkspaceClients(ctx)
		if err != nil {
			lastErr = err
			logger.Info("quota-provider virtual workspace not ready yet; retrying", "error", err.Error())

			return false, nil
		}

		count := 0
		for _, vwClient := range vwClients {
			var list v1alpha1.ConsumptionQuotaList
			if err := vwClient.List(ctx, &list); err != nil {
				lastErr = err
				logger.Info("initial ConsumptionQuota list not ready yet; retrying", "error", err.Error())

				return false, nil
			}

			for i := range list.Items {
				cq := &list.Items[i]
				if cq.Status.IdentityHash == "" || !cq.DeletionTimestamp.IsZero() {
					continue
				}
				cqRef := logicalcluster.From(cq).String() + "/" + cq.Name
				w.remember(cqRef, cq)
				count++
			}

			var grants v1alpha1.QuotaGrantList
			if err := vwClient.List(ctx, &grants); err != nil {
				lastErr = err
				logger.Info("initial QuotaGrant list not ready yet; retrying", "error", err.Error())

				return false, nil
			}

			for i := range grants.Items {
				g := &grants.Items[i]
				if !g.MirrorsOverride() {
					continue
				}
				w.reg.SetGrant(g.Spec.Consumer, registry.ResourceRef{
					Group:        g.Spec.Governed.Group,
					Resource:     g.Spec.Governed.Resource,
					IdentityHash: g.Spec.Governed.IdentityHash,
				}, *g.Spec.GrantedLimit)
			}
		}

		total, shards = count, len(vwClients)

		return true, nil
	})
	if waitErr != nil {
		// Surface the underlying cause (e.g. "has no endpoints yet") rather than
		// the bare poll-timeout, so a persistent misconfiguration is diagnosable.
		if lastErr != nil {
			return fmt.Errorf("waiting for quota-provider virtual workspace: %w", lastErr)
		}

		return fmt.Errorf("waiting for quota-provider virtual workspace: %w", waitErr)
	}

	logger.Info("registry populated", "quotaCount", total, "shards", shards)

	return nil
}

// virtualWorkspaceClients returns one client per advertised virtual-workspace
// endpoint (one per kcp shard), each pointing at {vwURL}/clusters/* so it can
// list ConsumptionQuotas across every provider workspace bound to the
// quota-provider APIExport on that shard. Listing only one endpoint would seed
// the registry with a single shard's quotas on a multi-shard kcp.
func (w *registryWatcher) virtualWorkspaceClients(ctx context.Context) ([]client.Client, error) {
	localCfg := w.mgr.GetLocalManager().GetConfig()

	directClient, err := client.New(localCfg, client.Options{Scheme: w.scheme})
	if err != nil {
		return nil, fmt.Errorf("creating direct client: %w", err)
	}

	urls, err := kcp.VirtualWorkspaceURLs(ctx, directClient, w.apiExportName)
	if err != nil {
		return nil, fmt.Errorf("resolving virtual workspace URLs for %s: %w", w.apiExportName, err)
	}

	clients := make([]client.Client, 0, len(urls))
	for _, url := range urls {
		vwClient, err := kcp.VWClient(localCfg, url, "*", w.scheme)
		if err != nil {
			return nil, fmt.Errorf("creating VW client for %s: %w", url, err)
		}
		clients = append(clients, vwClient)
	}

	return clients, nil
}

// readyzCheck reports healthy once the given channel is closed (i.e. the initial
// ConsumptionQuota list has synced into the registry). Fail-closed on startup.
func readyzCheck(initialized <-chan struct{}) func(*http.Request) error {
	return func(_ *http.Request) error {
		select {
		case <-initialized:
			return nil
		default:
			return fmt.Errorf("registry not yet populated")
		}
	}
}
