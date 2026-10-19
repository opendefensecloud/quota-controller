// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// grantedOf returns the resolved GrantedLimit as a value (or -1 for nil) so the
// table can assert on limits succinctly.
func grantedOf(s v1alpha1.QuotaGrantSpec) int32 {
	if s.GrantedLimit == nil {
		return -1
	}

	return *s.GrantedLimit
}

var _ = Describe("ResolveExistingGrant", func() {
	It("always mirrors the new requestedLimit onto the grant", func() {
		out := selfservice.ResolveExistingGrant(
			v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6))},
			5, new(int32(8)),
		)
		Expect(out.RequestedLimit).To(HaveValue(BeEquivalentTo(5)))
	})

	Context("standing rejection", func() {
		It("keeps a rejection when the identical ask is re-observed (no re-spam)", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantRejected, RequestedLimit: new(int32(9)), Reason: "denied by provider"}
			out := selfservice.ResolveExistingGrant(existing, 9, new(int32(8)))
			Expect(out.Decision).To(Equal(v1alpha1.GrantRejected))
			Expect(out.Reason).To(Equal("denied by provider"))
		})

		It("re-opens to Pending when a changed ask is still above the ceiling", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantRejected, RequestedLimit: new(int32(9)), GrantedLimit: new(int32(9))}
			out := selfservice.ResolveExistingGrant(existing, 10, new(int32(8)))
			Expect(out.Decision).To(Equal(v1alpha1.GrantPending))
			Expect(grantedOf(out)).To(BeEquivalentTo(-1)) // granted dropped
			Expect(out.Reason).To(Equal(selfservice.ReasonAwaitingApproval))
		})

		It("auto-approves when a changed ask drops to at/below the ceiling", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantRejected, RequestedLimit: new(int32(9))}
			out := selfservice.ResolveExistingGrant(existing, 7, new(int32(8)))
			Expect(out.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grantedOf(out)).To(BeEquivalentTo(7))
			Expect(out.Reason).To(Equal(selfservice.ReasonAutoApproved))
		})
	})

	Context("within-ceiling ask", func() {
		It("auto-approves and grants the requested limit", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantPending, RequestedLimit: new(int32(3))}
			out := selfservice.ResolveExistingGrant(existing, 4, new(int32(8)))
			Expect(out.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grantedOf(out)).To(BeEquivalentTo(4))
			Expect(out.Reason).To(Equal(selfservice.ReasonAutoApproved))
		})
	})

	Context("over-ceiling ask against a standing approval", func() {
		It("holds the approved limit and surfaces the escalation in the reason", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(8)), GrantedLimit: new(int32(8))}
			out := selfservice.ResolveExistingGrant(existing, 99, nil)
			Expect(out.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grantedOf(out)).To(BeEquivalentTo(8)) // held, not raised
			Expect(out.Reason).To(Equal("approved at 8; escalation to 99 awaits provider action"))
		})

		It("reports an incomplete approval when the approval carries no grantedLimit", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(4))}
			out := selfservice.ResolveExistingGrant(existing, 10, nil)
			Expect(out.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grantedOf(out)).To(BeEquivalentTo(-1))
			Expect(out.Reason).To(Equal(selfservice.ReasonApprovedNoLimit))
		})

		It("down-scales grantedLimit when the consumer lowers below the grant", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6))}
			out := selfservice.ResolveExistingGrant(existing, 5, nil)
			Expect(out.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grantedOf(out)).To(BeEquivalentTo(5))
			Expect(out.Reason).To(Equal("approved at 5"))
		})

		It("is a no-op reason when the request equals the granted limit", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantApproved, RequestedLimit: new(int32(7)), GrantedLimit: new(int32(6))}
			out := selfservice.ResolveExistingGrant(existing, 6, nil)
			Expect(grantedOf(out)).To(BeEquivalentTo(6))
			Expect(out.Reason).To(Equal("approved at 6"))
		})
	})

	Context("over-ceiling ask against a standing Pending grant", func() {
		It("leaves the grant Pending and untouched except for the mirrored request", func() {
			existing := v1alpha1.QuotaGrantSpec{Decision: v1alpha1.GrantPending, RequestedLimit: new(int32(9)), Reason: "awaiting"}
			out := selfservice.ResolveExistingGrant(existing, 12, nil)
			Expect(out.Decision).To(Equal(v1alpha1.GrantPending))
			Expect(out.Reason).To(Equal("awaiting"))
			Expect(out.RequestedLimit).To(HaveValue(BeEquivalentTo(12)))
		})
	})
})
