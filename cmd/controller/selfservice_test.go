// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"go.opendefense.cloud/quota-controller/internal/registry"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubEnsurer is a claimEnsurer test double that records every call and lets
// specs inject per-consumer errors, so sweep resilience can be verified
// without a real client.
type stubEnsurer struct {
	ensureErr map[string]error
	removeErr map[string]error

	ensureCalls []string
	removeCalls []string
}

func (s *stubEnsurer) Ensure(_ context.Context, consumer string, _ registry.ResourceRef, _ int32) error {
	s.ensureCalls = append(s.ensureCalls, consumer)

	return s.ensureErr[consumer]
}

func (s *stubEnsurer) Remove(_ context.Context, consumer string, _ registry.ResourceRef) error {
	s.removeCalls = append(s.removeCalls, consumer)

	return s.removeErr[consumer]
}

var _ = Describe("diffConsumers", func() {
	It("adds newly bound consumers and removes unbound ones", func() {
		add, remove := diffConsumers(sets.New("c1", "c2"), sets.New("c2", "c3"))
		Expect(add).To(ConsistOf("c1"))
		Expect(remove).To(ConsistOf("c3"))
	})
	It("is empty when sets match", func() {
		add, remove := diffConsumers(sets.New("c1"), sets.New("c1"))
		Expect(add).To(BeEmpty())
		Expect(remove).To(BeEmpty())
	})
})

var _ = Describe("claimDiscovery.apply", func() {
	var (
		d    *claimDiscovery
		stub *stubEnsurer
	)

	BeforeEach(func() {
		stub = &stubEnsurer{ensureErr: map[string]error{}, removeErr: map[string]error{}}
		d = &claimDiscovery{
			ensurer: stub,
			log:     logr.Discard(),
			ensured: sets.New[string](),
		}
	})

	It("continues past a per-consumer Ensure error and aggregates it (adversarial consumer must not starve the others)", func() {
		stub.ensureErr["c2"] = errors.New("boom")

		err := d.apply(context.Background(), []string{"c1", "c2", "c3"}, nil, 3)

		Expect(err).To(HaveOccurred())
		Expect(stub.ensureCalls).To(ConsistOf("c1", "c2", "c3"), "all three consumers must be attempted")
		Expect(d.ensured.Has("c1")).To(BeTrue())
		Expect(d.ensured.Has("c2")).To(BeFalse(), "the failing consumer must not be recorded as ensured")
		Expect(d.ensured.Has("c3")).To(BeTrue())
	})

	It("continues past a per-consumer Remove error, keeping the consumer in ensured for retry", func() {
		d.ensured.Insert("c1")
		d.ensured.Insert("c2")
		stub.removeErr["c1"] = errors.New("boom")

		err := d.apply(context.Background(), nil, []string{"c1", "c2"}, 3)

		Expect(err).To(HaveOccurred())
		Expect(stub.removeCalls).To(ConsistOf("c1", "c2"))
		Expect(d.ensured.Has("c1")).To(BeTrue(), "failed removal must stay ensured so the next sweep retries it")
		Expect(d.ensured.Has("c2")).To(BeFalse())
	})

	It("calls Ensure for every currently-bound consumer each sweep, even ones already ensured (I2: status freshness)", func() {
		d.ensured.Insert("c1")

		err := d.apply(context.Background(), []string{"c1", "c2"}, nil, 3)

		Expect(err).NotTo(HaveOccurred())
		Expect(stub.ensureCalls).To(ConsistOf("c1", "c2"), "c1 was already ensured but must still be re-ensured this sweep")
		Expect(d.ensured.Has("c1")).To(BeTrue())
		Expect(d.ensured.Has("c2")).To(BeTrue())
	})

	It("returns nil when nothing fails", func() {
		err := d.apply(context.Background(), []string{"c1"}, nil, 3)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("leaderGatedRunnable", func() {
	It("does not satisfy controller-runtime's hasCache detection", func() {
		r := leaderGatedRunnable(nil)
		_, hasCache := r.(interface{ GetCache() cache.Cache })
		Expect(hasCache).To(BeFalse())
	})
	It("does not opt out of leader election", func() {
		r := leaderGatedRunnable(nil)
		_, optsOut := r.(interface{ NeedLeaderElection() bool })
		Expect(optsOut).To(BeFalse(), "no NeedLeaderElection method means the default leader-gated group")
	})
})
