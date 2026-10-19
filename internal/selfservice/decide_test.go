// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice_test

import (
	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Decide", func() {
	It("auto-approves at the ceiling", func() {
		Expect(selfservice.Decide(8, new(int32(8)))).To(Equal(v1alpha1.GrantApproved))
	})
	It("auto-approves below the ceiling", func() {
		Expect(selfservice.Decide(5, new(int32(8)))).To(Equal(v1alpha1.GrantApproved))
	})
	It("routes above-ceiling requests to manual approval", func() {
		Expect(selfservice.Decide(9, new(int32(8)))).To(Equal(v1alpha1.GrantPending))
	})
	It("routes every request to manual approval when no ceiling is set", func() {
		Expect(selfservice.Decide(1, nil)).To(Equal(v1alpha1.GrantPending))
	})
})
