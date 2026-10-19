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

	if !g.MirrorsOverride() {
		// Pending/Rejected/incomplete/deleting grants must not raise or hold a limit.
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
