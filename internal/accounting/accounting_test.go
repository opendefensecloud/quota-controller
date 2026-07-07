// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package accounting_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/accounting"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var t0 = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

func res(key string, exp time.Time) v1alpha1.Reservation {
	return v1alpha1.Reservation{Key: key, ExpiresAt: metav1.NewTime(exp)}
}

var _ = Describe("Accounting", func() {
	It("counts confirmed plus live reservations", func() {
		st := v1alpha1.QuotaUsageStatus{
			Confirmed: 2,
			Reservations: []v1alpha1.Reservation{
				res("ns/a", t0.Add(time.Minute)),
				res("ns/b", t0.Add(-time.Minute)), // expired
			},
		}

		Expect(accounting.Used(st, t0)).To(BeNumerically("==", 3)) // 2 confirmed + 1 live (b expired)
	})

	It("grants reservation when below limit", func() {
		st := v1alpha1.QuotaUsageStatus{Confirmed: 2}

		out, ok := accounting.Reserve(st, "ns/new", 3, t0, time.Minute)

		Expect(ok).To(BeTrue())
		Expect(out.Reservations).To(HaveLen(1))
		Expect(out.Reservations[0].Key).To(Equal("ns/new"))
	})

	It("denies reservation at limit", func() {
		st := v1alpha1.QuotaUsageStatus{Confirmed: 3}

		_, ok := accounting.Reserve(st, "ns/new", 3, t0, time.Minute)

		Expect(ok).To(BeFalse())
	})

	It("reserve is idempotent for the same key", func() {
		st := v1alpha1.QuotaUsageStatus{
			Confirmed:    2,
			Reservations: []v1alpha1.Reservation{res("ns/x", t0.Add(time.Minute))},
		}

		out, ok := accounting.Reserve(st, "ns/x", 3, t0, time.Minute)

		Expect(ok).To(BeTrue())
		Expect(out.Reservations).To(HaveLen(1))
	})

	It("sweep drops expired reservations", func() {
		st := v1alpha1.QuotaUsageStatus{Reservations: []v1alpha1.Reservation{
			res("ns/live", t0.Add(time.Minute)),
			res("ns/dead", t0.Add(-time.Second)),
		}}

		out := accounting.Sweep(st, t0)

		Expect(out.Reservations).To(HaveLen(1))
		Expect(out.Reservations[0].Key).To(Equal("ns/live"))
	})

	It("fold confirmed removes observed reservations", func() {
		st := v1alpha1.QuotaUsageStatus{
			Confirmed: 1,
			Reservations: []v1alpha1.Reservation{
				res("ns/seen", t0.Add(time.Minute)),
				res("ns/pending", t0.Add(time.Minute)),
			},
		}

		out := accounting.FoldConfirmed(st, 2, sets.New("ns/seen"))

		Expect(out.Confirmed).To(BeNumerically("==", 2))
		Expect(out.Reservations).To(HaveLen(1))
		Expect(out.Reservations[0].Key).To(Equal("ns/pending"))
	})
})
