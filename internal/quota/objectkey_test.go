// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package quota_test

import (
	"go.opendefense.cloud/quota-controller/internal/quota"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ObjectKey is the single source of truth for reservation keys: the admission
// webhook reserves under it and the accounting reconciler folds live objects
// under it. Both sides MUST use it, or fulfilled reservations are never dropped
// and double-count until the TTL sweep.
var _ = Describe("ObjectKey", func() {
	It("returns namespace/name for namespaced objects", func() {
		Expect(quota.ObjectKey("ns", "obj")).To(Equal("ns/obj"))
	})

	It("returns the bare name for cluster-scoped objects", func() {
		Expect(quota.ObjectKey("", "obj")).To(Equal("obj"))
	})
})
