// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"go.opendefense.cloud/quota-controller/internal/identity"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("UsageKey", func() {
	It("returns a deterministic, valid object name and distinct keys do not collide", func() {
		k := identity.UsageKey{Cluster: "root:c1", Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc"}
		got := k.ObjectName()

		Expect(got).To(Equal(k.ObjectName())) // deterministic

		Expect(got).NotTo(BeEmpty())
		Expect(len(got)).To(BeNumerically("<=", 63))
		Expect(got).To(HavePrefix("qu-"))

		other := identity.UsageKey{Cluster: "root:c2", Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc"}

		Expect(other.ObjectName()).NotTo(Equal(got)) // distinct keys must not collide
	})
})
