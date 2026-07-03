// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package quota_test

import (
	"context"
	"fmt"
	"time"

	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Store.Reserve", func() {
	key := identity.UsageKey{Cluster: "root:c1", Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	It("creates the ledger and grants up to the limit, then denies", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		ctx := context.Background()

		ok1, err := s.Reserve(ctx, key, "ns/a", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok1).To(BeTrue())

		ok2, err := s.Reserve(ctx, key, "ns/b", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok2).To(BeTrue())

		ok3, err := s.Reserve(ctx, key, "ns/c", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok3).To(BeFalse()) // at limit
	})
})

var _ = Describe("Store.Reserve under concurrency", func() {
	It("never grants more than the limit for parallel reservations", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		key := identity.UsageKey{Cluster: "root:c2", Group: "s3.example.com", Resource: "buckets", IdentityHash: "h2"}

		const limit, n = 5, 20

		granted := make(chan bool, n)

		for i := range n {
			go func(i int) {
				defer GinkgoRecover()

				ok, err := s.Reserve(context.Background(), key, fmt.Sprintf("ns/o%d", i), limit)
				Expect(err).NotTo(HaveOccurred())
				granted <- ok
			}(i)
		}

		count := 0
		for range n {
			if <-granted {
				count++
			}
		}

		Expect(count).To(Equal(limit)) // exactly limit granted, never more
	})
})

var _ = Describe("Store.Reserve stress test beyond old CAS cap", func() {
	It("grants exactly the limit with high concurrency well beyond the old retry cap", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		key := identity.UsageKey{Cluster: "root:c3", Group: "s3.example.com", Resource: "buckets", IdentityHash: "h3"}

		const limit, n = 15, 60

		granted := make(chan bool, n)

		for i := range n {
			go func(i int) {
				defer GinkgoRecover()

				ok, err := s.Reserve(context.Background(), key, fmt.Sprintf("ns/s%d", i), limit)
				Expect(err).NotTo(HaveOccurred())
				granted <- ok
			}(i)
		}

		count := 0
		for range n {
			if <-granted {
				count++
			}
		}

		Expect(count).To(Equal(15)) // exactly 15 granted, no more
	})
})

// generateName regression: each generateName admission request is identified by a
// unique per-request UID ("uid:<UID>"). This test fires N concurrent Store.Reserve
// calls using that key format (as the webhook would) and asserts exactly K are
// granted — proving that the uid-keyed approach respects the limit under concurrency.
var _ = Describe("Store.Reserve for generateName uid-keyed reservations", func() {
	It("grants exactly the limit for concurrent uid-keyed reservations (generateName regression)", func() {
		s := &quota.Store{Client: k8sClient, TTL: time.Minute, Now: time.Now}
		key := identity.UsageKey{Cluster: "root:gen1", Group: "s3.example.com", Resource: "buckets", IdentityHash: "hgen1"}

		const limit, n = 3, 12

		granted := make(chan bool, n)

		for i := range n {
			go func(i int) {
				defer GinkgoRecover()
				// Mirror the objKey format that Handle uses for generateName requests.
				objKey := fmt.Sprintf("uid:%d", i)
				ok, err := s.Reserve(context.Background(), key, objKey, limit)
				Expect(err).NotTo(HaveOccurred())
				granted <- ok
			}(i)
		}

		count := 0
		for range n {
			if <-granted {
				count++
			}
		}

		Expect(count).To(Equal(limit),
			"generateName burst must respect limit: exactly %d granted out of %d", limit, n)
	})
})
