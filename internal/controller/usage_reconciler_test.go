// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// reservation is a helper to build a non-expired Reservation.
func reservation(key string) v1alpha1.Reservation {
	return v1alpha1.Reservation{Key: key, ExpiresAt: metav1.NewTime(time.Now().Add(time.Minute))}
}

var _ = Describe("ComputeConfirmed", func() {
	// TestComputeConfirmed_FoldsObservedReservation: when a live object matches an
	// existing reservation, the reservation is dropped and confirmed is set to the live count.
	It("folds an observed reservation and sets confirmed to the live count", func() {
		start := v1alpha1.QuotaUsageStatus{
			Confirmed:    0,
			Reservations: []v1alpha1.Reservation{reservation("ns/a")},
		}

		out := controller.ComputeConfirmed(start, []string{"ns/a"})

		Expect(out.Confirmed).To(BeNumerically("==", 1))
		Expect(out.Reservations).To(BeEmpty())
	})

	// TestComputeConfirmed_CountsKeyWithNoReservation: a newly observed object that was
	// never reserved still contributes to confirmed.
	It("counts a key that was never reserved", func() {
		start := v1alpha1.QuotaUsageStatus{
			Confirmed:    0,
			Reservations: nil,
		}

		out := controller.ComputeConfirmed(start, []string{"ns/b"})

		Expect(out.Confirmed).To(BeNumerically("==", 1))
		Expect(out.Reservations).To(BeEmpty())
	})

	// TestComputeConfirmed_KeepsUnobservedReservation: a reservation whose object has not
	// yet been observed is not dropped (the object is still in flight).
	It("keeps a reservation for an object not yet observed", func() {
		start := v1alpha1.QuotaUsageStatus{
			Confirmed:    0,
			Reservations: []v1alpha1.Reservation{reservation("ns/c")},
		}

		out := controller.ComputeConfirmed(start, []string{}) // no live keys observed

		Expect(out.Confirmed).To(BeNumerically("==", 0))
		Expect(out.Reservations).To(HaveLen(1))
		Expect(out.Reservations[0].Key).To(Equal("ns/c"))
	})
})
