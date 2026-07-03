// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("UsageReconciler integration", func() {
	const (
		group        = "s3.example.com"
		resource     = "buckets"
		identityHash = "inttest-hash-01"
	)

	newStore := func() *quota.Store {
		return &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
	}

	newReconciler := func(store *quota.Store, liveKeys []string) *controller.UsageReconciler {
		return &controller.UsageReconciler{
			Store:          store,
			Group:          group,
			Resource:       resource,
			IdentityHash:   identityHash,
			ResyncInterval: time.Minute,
			ListLiveKeys: func(_ context.Context, _ string) ([]string, error) {
				return liveKeys, nil
			},
		}
	}

	fetchStatus := func(ctx context.Context, key identity.UsageKey) v1alpha1.QuotaUsageStatus {
		GinkgoHelper()
		u := &v1alpha1.QuotaUsage{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: key.ObjectName()}, u)).To(Succeed())

		return u.Status
	}

	// Scenario A: a pre-created reservation is folded once the governed object is observed live.
	It("Scenario A: folds reservation when governed object is observed live (confirmed==1)", func() {
		ctx := context.Background()
		store := newStore()
		key := identity.UsageKey{
			Cluster:      "root:inttest-a",
			Group:        group,
			Resource:     resource,
			IdentityHash: identityHash,
		}

		// Pre-create a reservation for ns/a.
		ok, err := store.Reserve(ctx, key, "ns/a", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		// Sanity: reservation is present before reconcile.
		Expect(fetchStatus(ctx, key).Reservations).To(HaveLen(1))

		// Reconcile with ns/a live → reservation should fold and confirmed set to 1.
		Expect(newReconciler(store, []string{"ns/a"}).ReconcileCluster(ctx, key.Cluster)).To(Succeed())

		st := fetchStatus(ctx, key)
		Expect(st.Confirmed).To(BeNumerically("==", 1))
		Expect(st.Reservations).To(BeEmpty())
	})

	// Scenario B: two live objects with no prior reservations → confirmed==2.
	It("Scenario B: sets confirmed to live count with no reservations (confirmed==2)", func() {
		ctx := context.Background()
		key := identity.UsageKey{
			Cluster:      "root:inttest-b",
			Group:        group,
			Resource:     resource,
			IdentityHash: identityHash,
		}

		Expect(newReconciler(newStore(), []string{"ns/a", "ns/b"}).ReconcileCluster(ctx, key.Cluster)).To(Succeed())

		st := fetchStatus(ctx, key)
		Expect(st.Confirmed).To(BeNumerically("==", 2))
		Expect(st.Reservations).To(BeEmpty())
	})

	// Scenario C: all governed objects disappear → confirmed drops to zero.
	It("Scenario C: confirmed drops to zero when all objects disappear", func() {
		ctx := context.Background()
		store := newStore()
		key := identity.UsageKey{
			Cluster:      "root:inttest-c",
			Group:        group,
			Resource:     resource,
			IdentityHash: identityHash,
		}

		// Seed: two live objects.
		Expect(newReconciler(store, []string{"ns/x", "ns/y"}).ReconcileCluster(ctx, key.Cluster)).To(Succeed())
		Expect(fetchStatus(ctx, key).Confirmed).To(BeNumerically("==", 2))

		// All objects disappear.
		Expect(newReconciler(store, []string{}).ReconcileCluster(ctx, key.Cluster)).To(Succeed())

		st := fetchStatus(ctx, key)
		Expect(st.Confirmed).To(BeNumerically("==", 0))
		Expect(st.Reservations).To(BeEmpty())
	})
})
