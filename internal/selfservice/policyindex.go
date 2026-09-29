// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice

import (
	"sync"

	"go.opendefense.cloud/quota-controller/internal/registry"
)

// Policy is what the claim reconciler needs to know about the owning
// ConsumptionQuota of a governed ref: where it lives and its limits. Fed by
// the CQ reconcile path (controller binary), consumed on claim changes.
type Policy struct {
	ProviderCluster    string
	CQName             string
	DefaultLimit       int32
	AutoApproveCeiling *int32
}

type PolicyIndex struct {
	mu       sync.RWMutex
	policies map[registry.ResourceRef]Policy
}

func NewPolicyIndex() *PolicyIndex {
	return &PolicyIndex{policies: map[registry.ResourceRef]Policy{}}
}

func (i *PolicyIndex) Set(ref registry.ResourceRef, p Policy) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.policies[ref] = p
}

func (i *PolicyIndex) Delete(ref registry.ResourceRef) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.policies, ref)
}

func (i *PolicyIndex) Get(ref registry.ResourceRef) (Policy, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	p, ok := i.policies[ref]

	return p, ok
}
