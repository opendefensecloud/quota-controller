// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sync"
	"testing"

	"github.com/go-logr/logr"

	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

func newTestDispatcher() *validatorDispatcher {
	return newValidatorDispatcher(registry.New(), &quota.Store{}, make(chan struct{}), logr.Discard())
}

// TestHandlerForCaches asserts the per-ref handler is constructed once and the
// same instance is returned on subsequent lookups.
func TestHandlerForCaches(t *testing.T) {
	d := newTestDispatcher()
	ref := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}

	first := d.handlerFor(ref)
	second := d.handlerFor(ref)

	if first != second {
		t.Fatalf("handlerFor returned a different instance on the second call: %p vs %p", first, second)
	}
}

// TestHandlerForDistinctRefs asserts distinct refs get distinct handlers.
func TestHandlerForDistinctRefs(t *testing.T) {
	d := newTestDispatcher()
	a := registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
	b := registry.ResourceRef{Group: "s3.example.com", Resource: "objects", IdentityHash: "h2"}

	if d.handlerFor(a) == d.handlerFor(b) {
		t.Fatal("distinct refs must not share a handler")
	}
}

// TestHandlerForConcurrent hammers handlerFor from many goroutines over a small
// ref set. Run under -race, it guards the double-checked-locking cache against
// data races and confirms a stable single instance survives the contention.
func TestHandlerForConcurrent(t *testing.T) {
	d := newTestDispatcher()
	refs := []registry.ResourceRef{
		{Group: "g", Resource: "r0", IdentityHash: "h0"},
		{Group: "g", Resource: "r1", IdentityHash: "h1"},
		{Group: "g", Resource: "r2", IdentityHash: "h2"},
	}

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			for j := range 200 {
				_ = d.handlerFor(refs[(i+j)%len(refs)])
			}
		}(i)
	}
	wg.Wait()

	// After the storm every ref must resolve to exactly one cached handler.
	for _, ref := range refs {
		if d.handlerFor(ref) != d.handlerFor(ref) {
			t.Fatalf("ref %v resolved to two handlers after concurrent construction", ref)
		}
	}
	if len(d.handlers) != len(refs) {
		t.Fatalf("expected %d cached handlers, got %d", len(refs), len(d.handlers))
	}
}
