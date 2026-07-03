// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package registry is the webhook's in-memory (group,resource,identity)->limit index,
// fed by the ConsumptionQuota controller (spec §9).
package registry

import "sync"

type ResourceRef struct {
	Group        string
	Resource     string
	IdentityHash string
}

type Registry struct {
	mu     sync.RWMutex
	limits map[ResourceRef]int32
}

func New() *Registry { return &Registry{limits: map[ResourceRef]int32{}} }

func (r *Registry) Set(ref ResourceRef, defaultLimit int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits[ref] = defaultLimit
}

func (r *Registry) Delete(ref ResourceRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.limits, ref)
}

// LimitFor returns the effective limit for a consumer. Phase 1: the default only.
// Phase 2 will consult per-consumer grants here (cluster is already in the signature).
func (r *Registry) LimitFor(_ string, ref ResourceRef) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.limits[ref]

	return l, ok
}

func (r *Registry) ByGroupResource(group, resource string) []ResourceRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ResourceRef
	for ref := range r.limits {
		if ref.Group == group && ref.Resource == resource {
			out = append(out, ref)
		}
	}

	return out
}
