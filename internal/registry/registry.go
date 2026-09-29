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

// grantKey addresses one consumer's override for one governed resource.
type grantKey struct {
	Cluster string
	Ref     ResourceRef
}

type Registry struct {
	mu     sync.RWMutex
	limits map[ResourceRef]int32
	grants map[grantKey]int32
}

func New() *Registry { return &Registry{limits: map[ResourceRef]int32{}, grants: map[grantKey]int32{}} }

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

// SetGrant records an APPROVED per-consumer override (spec §9). Pending and
// Rejected grants must never be Set — callers delete instead.
func (r *Registry) SetGrant(cluster string, ref ResourceRef, limit int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grants[grantKey{Cluster: cluster, Ref: ref}] = limit
}

func (r *Registry) DeleteGrant(cluster string, ref ResourceRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.grants, grantKey{Cluster: cluster, Ref: ref})
}

// LimitFor returns the effective limit for a consumer (spec §9): an approved
// grant wins; otherwise the policy default. ok=false means neither exists —
// the webhook fails closed on that (ADR-003).
func (r *Registry) LimitFor(cluster string, ref ResourceRef) (int32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if l, ok := r.grants[grantKey{Cluster: cluster, Ref: ref}]; ok {
		return l, true
	}
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
