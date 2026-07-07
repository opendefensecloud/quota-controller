// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"

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

// testGovernedCQ builds a ConsumptionQuota whose governed export maps to
// testGoverned/testKey below.
func testGovernedCQ(name string) *v1alpha1.ConsumptionQuota {
	return &v1alpha1.ConsumptionQuota{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.ConsumptionQuotaSpec{
			Governed: v1alpha1.GovernedResource{
				APIExportName: "s3-export",
				Group:         "s3.example.com",
				Version:       "v1",
				Resource:      "buckets",
			},
			By:           "Count",
			DefaultLimit: 1,
		},
		Status: v1alpha1.ConsumptionQuotaStatus{IdentityHash: "hash0123456789"},
	}
}

var (
	testGoverned = governed{
		group: "s3.example.com", version: "v1", resource: "buckets",
		identityHash: "hash0123456789", apiExportName: "s3-export",
	}
	testKey = accountingKey("s3.example.com", "buckets", "hash0123456789")
)

// These specs exercise the lifecycle state machine (Ensure dedupe/rollback,
// Remove teardown, retryStart after an unexpected self-stop) through the
// startFn seam. The fake mirrors the one contract real start() guarantees:
// sets[key] is registered before a successful return.
var _ = Describe("accountingManager lifecycle", func() {
	var (
		am       *accountingManager
		mu       sync.Mutex
		calls    int
		failures int // number of leading startFn calls that fail
		canceled bool
	)

	ctx := context.Background()

	newTestManager := func(root context.Context) *accountingManager {
		a := newAccountingManager(root, &rest.Config{}, runtime.NewScheme(), nil, time.Minute, logr.Discard())
		a.retryBackoff = wait.Backoff{Duration: time.Millisecond, Factor: 1.0, Steps: 1000}
		a.startFn = func(_ context.Context, _ string, _ governed, key string) (*accountingSet, error) {
			mu.Lock()
			calls++
			fail := calls <= failures
			mu.Unlock()
			if fail {
				return nil, errors.New("start failed")
			}

			set := &accountingSet{cancel: func() {
				mu.Lock()
				canceled = true
				mu.Unlock()
			}}
			a.mu.Lock()
			a.sets[key] = set
			a.mu.Unlock()

			return set, nil
		}

		return a
	}

	BeforeEach(func() {
		calls, failures, canceled = 0, 0, false
		am = newTestManager(context.Background())
	})

	Describe("Ensure", func() {
		It("starts one set per governed export and reference-counts additional CQs", func() {
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-a"))).To(Succeed())
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-b"))).To(Succeed())

			Expect(calls).To(Equal(1))
		})

		It("rolls back the reference when start fails so a later Ensure retries", func() {
			failures = 1

			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-a"))).NotTo(Succeed())
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-a"))).To(Succeed())

			Expect(calls).To(Equal(2))
		})
	})

	Describe("Remove", func() {
		It("tears the set down only when the last referencing CQ is removed", func() {
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-a"))).To(Succeed())
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-b"))).To(Succeed())

			am.Remove("root:p1", "cq-a")
			Expect(canceled).To(BeFalse())

			am.Remove("root:p1", "cq-b")
			Expect(canceled).To(BeTrue())

			// A fresh Ensure after teardown must start a new set.
			Expect(am.Ensure(ctx, "root:p1", testGovernedCQ("cq-c"))).To(Succeed())
			Expect(calls).To(Equal(2))
		})
	})

	Describe("retryStart", func() {
		// Prime refs[key] the way an unexpected self-stop leaves it: references
		// still live, set deregistered.
		primeRefs := func() {
			am.mu.Lock()
			am.refs[testKey] = sets.New("root:p1/cq-a")
			am.mu.Unlock()
		}

		It("relaunches the set after transient start failures", func() {
			failures = 2
			primeRefs()

			am.retryStart(testGoverned, testKey, "root:p1")

			Expect(calls).To(Equal(3))
			am.mu.Lock()
			_, running := am.sets[testKey]
			am.mu.Unlock()
			Expect(running).To(BeTrue())
		})

		It("exits without starting when all references were removed while waiting", func() {
			am.retryStart(testGoverned, testKey, "root:p1")

			Expect(calls).To(BeZero())
		})

		It("defers to a replacement set started concurrently by Ensure", func() {
			primeRefs()
			am.mu.Lock()
			am.sets[testKey] = &accountingSet{cancel: func() {}}
			am.mu.Unlock()

			am.retryStart(testGoverned, testKey, "root:p1")

			Expect(calls).To(BeZero())
		})

		It("aborts when the root context is done", func() {
			root, cancel := context.WithCancel(context.Background())
			cancel()
			am = newTestManager(root)
			// A delay that never elapses makes the ctx.Done branch deterministic.
			am.retryBackoff = wait.Backoff{Duration: time.Hour, Steps: 1}
			primeRefs()

			am.retryStart(testGoverned, testKey, "root:p1")

			Expect(calls).To(BeZero())
		})
	})
})
