// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cqLifecycle policy tracking", func() {
	var (
		c    *cqLifecycle
		refA = registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "hA"}
		refB = registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "hB"}
	)

	BeforeEach(func() {
		c = &cqLifecycle{
			policies:     selfservice.NewPolicyIndex(),
			cqPolicyRefs: map[string]registry.ResourceRef{},
		}
	})

	It("publishes a policy for a governed ref", func() {
		c.setPolicy("root:c1/cq", refA, selfservice.Policy{CQName: "cq", DefaultLimit: 3})

		got, ok := c.policies.Get(refA)
		Expect(ok).To(BeTrue())
		Expect(got.DefaultLimit).To(BeEquivalentTo(3))
	})

	It("drops the stale entry when a CQ switches governed ref (identity rotation)", func() {
		c.setPolicy("root:c1/cq", refA, selfservice.Policy{CQName: "cq", DefaultLimit: 3})
		c.setPolicy("root:c1/cq", refB, selfservice.Policy{CQName: "cq", DefaultLimit: 4})

		_, okA := c.policies.Get(refA)
		gotB, okB := c.policies.Get(refB)
		Expect(okA).To(BeFalse(), "old-identity policy must be deleted so its claims stop being authorized")
		Expect(okB).To(BeTrue())
		Expect(gotB.DefaultLimit).To(BeEquivalentTo(4))
	})

	It("refreshes in place (no delete) when the same ref is re-published", func() {
		c.setPolicy("root:c1/cq", refA, selfservice.Policy{CQName: "cq", DefaultLimit: 3})
		c.setPolicy("root:c1/cq", refA, selfservice.Policy{CQName: "cq", DefaultLimit: 9})

		got, ok := c.policies.Get(refA)
		Expect(ok).To(BeTrue())
		Expect(got.DefaultLimit).To(BeEquivalentTo(9))
	})

	It("removes a published policy on dropPolicy", func() {
		c.setPolicy("root:c1/cq", refA, selfservice.Policy{CQName: "cq", DefaultLimit: 3})
		c.dropPolicy("root:c1/cq")

		_, ok := c.policies.Get(refA)
		Expect(ok).To(BeFalse())
		Expect(c.cqPolicyRefs).NotTo(HaveKey("root:c1/cq"))
	})

	It("is a no-op when dropping an unknown CQ", func() {
		Expect(func() { c.dropPolicy("root:c1/never-seen") }).NotTo(Panic())
	})
})
