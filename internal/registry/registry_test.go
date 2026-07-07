// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"go.opendefense.cloud/quota-controller/internal/registry"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Registry", func() {
	It("sets a limit and deletes it", func() {
		r := registry.New()
		ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
		r.Set(ref, 3)

		got, ok := r.LimitFor("root:c1", ref)

		Expect(ok).To(BeTrue())
		Expect(got).To(BeNumerically("==", 3))

		r.Delete(ref)

		_, ok = r.LimitFor("root:c1", ref)

		Expect(ok).To(BeFalse())
	})

	It("returns all refs for a given group/resource", func() {
		r := registry.New()
		r.Set(registry.ResourceRef{Group: "g", Resource: "r", IdentityHash: "a"}, 1)
		r.Set(registry.ResourceRef{Group: "g", Resource: "r", IdentityHash: "b"}, 2)

		Expect(r.ByGroupResource("g", "r")).To(HaveLen(2))
	})
})
