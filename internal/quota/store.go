// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package quota wraps the pure accounting logic in a compare-and-swap loop against
// the QuotaUsage objects in the quota-ctrl workspace (spec §7.1).
package quota

import (
	"context"
	"fmt"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"
	"go.opendefense.cloud/quota-controller/internal/identity"
)

// Store wraps the pure accounting logic in a get-or-create + CAS retry loop.
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
// optimistic concurrency, retrying on conflict with exponential backoff bounded by ctx.
func (s *Store) Apply(ctx context.Context, key identity.UsageKey, mutate func(v1alpha1.QuotaUsageStatus) v1alpha1.QuotaUsageStatus) error {
	name := key.ObjectName()

	backoff := wait.Backoff{
		Duration: 5 * time.Millisecond,
		Factor:   1.5,
		Jitter:   0.5,
		Steps:    30,
		Cap:      250 * time.Millisecond,
	}

	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		u := &v1alpha1.QuotaUsage{}

		gErr := s.Client.Get(ctx, client.ObjectKey{Name: name}, u)

		switch {
		case apierrors.IsNotFound(gErr):
			u = &v1alpha1.QuotaUsage{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: v1alpha1.QuotaUsageSpec{
					Consumer:     key.Cluster,
					Group:        key.Group,
					Resource:     key.Resource,
					IdentityHash: key.IdentityHash,
				},
			}

			if cErr := s.Client.Create(ctx, u); cErr != nil {
				if apierrors.IsAlreadyExists(cErr) {
					return false, nil // someone else created it; re-GET next iteration
				}

				return false, cErr
			}

		case gErr != nil:
			return false, gErr
		}

		before := u.Status
		u.Status = mutate(u.Status)

		if apiequality.Semantic.DeepEqual(before, u.Status) {
			return true, nil // nothing to write (deny or idempotent path)
		}

		if uErr := s.Client.Status().Update(ctx, u); uErr != nil {
			if apierrors.IsConflict(uErr) {
				return false, nil // CAS lost; retry
			}

			return false, uErr
		}

		return true, nil
	})
	if err != nil {
		log.FromContext(ctx).Error(err, "quota: CAS did not converge", "usage", name)

		return fmt.Errorf("quota: CAS did not converge for %s: %w", name, err)
	}

	return nil
}
