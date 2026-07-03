// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

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
// confirmed is set to len(liveKeys) and any reservation whose object is now live
// is dropped (fulfilled-reservation hand-off, spec §7.2).
func ComputeConfirmed(st v1alpha1.QuotaUsageStatus, liveKeys []string) v1alpha1.QuotaUsageStatus {
	set := sets.New(liveKeys...)
	//nolint:gosec // len(liveKeys) will never approach int32 max in practice
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
	// cluster (from the multicluster informer cache; wired in cmd/controller, Task 10).
	ListLiveKeys func(ctx context.Context, cluster string) ([]string, error)
}

// ReconcileCluster recomputes confirmed for one consumer cluster by listing all live
// governed objects and applying FoldConfirmed in a single CAS write.
func (r *UsageReconciler) ReconcileCluster(ctx context.Context, cluster string) error {
	keys, err := r.ListLiveKeys(ctx, cluster)
	if err != nil {
		return err
	}

	key := identity.UsageKey{
		Cluster:      cluster,
		Group:        r.Group,
		Resource:     r.Resource,
		IdentityHash: r.IdentityHash,
	}

	return r.Store.Apply(ctx, key, func(st v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus {
		return ComputeConfirmed(st, keys)
	})
}

// SweepAll drops expired reservations from every QuotaUsage. Intended to be called
// on a ticker driven by ResyncInterval.
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
