// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("QuotaGrant predicates", func() {
	Describe("MirrorsOverride", func() {
		It("is true only for a live, Approved grant with a GrantedLimit", func() {
			g := &v1alpha1.QuotaGrant{
				Spec: v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, GrantedLimit: new(int32(5))},
			}
			Expect(g.MirrorsOverride()).To(BeTrue())
		})

		It("is false when the decision is not Approved", func() {
			for _, d := range []v1alpha1.GrantDecision{v1alpha1.GrantPending, v1alpha1.GrantRejected} {
				g := &v1alpha1.QuotaGrant{
					Spec: v1alpha1.QuotaGrantSpec{Decision: d, GrantedLimit: new(int32(5))},
				}
				Expect(g.MirrorsOverride()).To(BeFalse(), "decision %s", d)
			}
		})

		It("is false for an Approved grant that carries no GrantedLimit", func() {
			g := &v1alpha1.QuotaGrant{
				Spec: v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved},
			}
			Expect(g.MirrorsOverride()).To(BeFalse())
		})

		It("is false while the grant is being deleted", func() {
			now := metav1.Now()
			g := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now, Finalizers: []string{"x"}},
				Spec:       v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, GrantedLimit: new(int32(5))},
			}
			Expect(g.MirrorsOverride()).To(BeFalse())
		})
	})

	Describe("AwaitsProvider", func() {
		It("is false for a non-Approved grant regardless of limits", func() {
			for _, d := range []v1alpha1.GrantDecision{v1alpha1.GrantPending, v1alpha1.GrantRejected} {
				s := v1alpha1.QuotaGrantSpec{Decision: d, RequestedLimit: new(int32(10)), GrantedLimit: new(int32(5))}
				Expect(s.AwaitsProvider()).To(BeFalse(), "decision %s", d)
			}
		})

		It("is true for an Approved grant with no GrantedLimit yet", func() {
			s := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(3))}
			Expect(s.AwaitsProvider()).To(BeTrue())
		})

		It("is true for an unresolved raise (requested > granted)", func() {
			s := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(10)), GrantedLimit: new(int32(6))}
			Expect(s.AwaitsProvider()).To(BeTrue())
		})

		It("is false when the granted limit meets or exceeds the request", func() {
			s := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6))}
			Expect(s.AwaitsProvider()).To(BeFalse())
		})
	})
})
