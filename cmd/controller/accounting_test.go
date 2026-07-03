// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("governedControllerName", func() {
	It("derives distinct names for governed exports that differ only by identity", func() {
		// Regression: two ConsumptionQuotas naming the same resource under
		// different APIExport identities must not share a controller name, or the
		// second accounting sub-manager fails to build with
		// "controller with name governed-usage already exists".
		a := governed{group: "s3.example.com", resource: "buckets", identityHash: "abcdef0123456789"}
		b := governed{group: "s3.example.com", resource: "buckets", identityHash: "0123456789abcdef"}

		Expect(governedControllerName(a)).NotTo(Equal(governedControllerName(b)))
		Expect(governedControllerName(a)).To(Equal("governed-usage-buckets-abcdef01"))
	})

	It("tolerates identity hashes shorter than the truncation window", func() {
		g := governed{group: "s3.example.com", resource: "buckets", identityHash: "abc"}

		Expect(governedControllerName(g)).To(Equal("governed-usage-buckets-abc"))
	})
})
