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

var _ = Describe("per-consumer grants", func() {
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("prefers an approved grant over the default limit", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(8)))
	})

	It("falls back to the default for consumers without a grant", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:other", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(3)))
	})

	It("reverts to the default when a grant is deleted", func() {
		r := registry.New()
		r.Set(ref, 3)
		r.SetGrant("root:c1", ref, 8)
		r.DeleteGrant("root:c1", ref)

		l, _ := r.LimitFor("root:c1", ref)
		Expect(l).To(Equal(int32(3)))
	})

	It("resolves a grant even when no default is registered yet", func() {
		r := registry.New()
		r.SetGrant("root:c1", ref, 8)

		l, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeTrue())
		Expect(l).To(Equal(int32(8)))
	})

	It("returns not-ok when neither grant nor default exists (fail-closed)", func() {
		r := registry.New()

		_, ok := r.LimitFor("root:c1", ref)
		Expect(ok).To(BeFalse())
	})
})
