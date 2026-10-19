// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PolicyIndex", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("stores and retrieves the owning policy for a governed ref", func() {
		idx := selfservice.NewPolicyIndex()
		idx.Set(ref, selfservice.Policy{ProviderCluster: "root:p1", CQName: "bucket-quota", DefaultLimit: 3})

		p, ok := idx.Get(ref)
		Expect(ok).To(BeTrue())
		Expect(p.ProviderCluster).To(Equal("root:p1"))
		Expect(p.CQName).To(Equal("bucket-quota"))
	})

	It("misses after Delete", func() {
		idx := selfservice.NewPolicyIndex()
		idx.Set(ref, selfservice.Policy{ProviderCluster: "root:p1"})
		idx.Delete(ref)

		_, ok := idx.Get(ref)
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("deterministic names", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "abcdef0123456789"}

	It("derives a stable claim name per governed ref", func() {
		Expect(selfservice.ClaimName(ref)).To(Equal("qc-buckets-abcdef01"))
	})
	It("derives a stable grant name per (ref, consumer) by hashing the consumer", func() {
		Expect(selfservice.GrantName(ref, "2ho8elmi0zqmgnbi")).To(Equal("qg-buckets-abcdef01-046029657f6cc846"))
	})
	It("keeps names within DNS limits for long resource names", func() {
		long := registry.ResourceRef{Group: "g", Resource: "validatingadmissionpolicybindings", IdentityHash: "abcdef0123456789"}
		Expect(len(selfservice.ClaimName(long))).To(BeNumerically("<=", 63))
	})
	It("never collides two distinct consumers that would substitution-collide on ':'", func() {
		Expect(selfservice.GrantName(ref, "root:a-b")).NotTo(Equal(selfservice.GrantName(ref, "root-a:b")))
	})
	It("produces a bounded, RFC1123-valid name for a long, colon-and-slash-heavy consumer path", func() {
		longConsumer := "root:orgs:acme-corp:teams:platform-engineering:clusters:c1234567890"
		name := selfservice.GrantName(ref, longConsumer)
		Expect(len(name)).To(BeNumerically("<=", 63))
		Expect(name).To(MatchRegexp(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`))
	})
})
