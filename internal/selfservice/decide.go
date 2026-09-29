// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package selfservice holds the pure decision and indexing primitives for the
// 1b request/approval workflow (spec §8) — no Kubernetes client in here.
package selfservice

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// Decide implements the §8 auto-approval rule: at/below the provider's
// ceiling → Approved; above it, or when no ceiling is set → Pending (manual).
func Decide(requested int32, ceiling *int32) v1alpha1.GrantDecision {
	if ceiling != nil && requested <= *ceiling {
		return v1alpha1.GrantApproved
	}

	return v1alpha1.GrantPending
}

// ClaimName returns the deterministic per-governed-ref QuotaClaim name in a
// consumer workspace. Resource truncated to 20 and identity to 8 chars keeps
// the name well inside the 63-char DNS label limit while staying unique per
// identity (same scheme as webhookEntryName's budget reasoning).
func ClaimName(ref registry.ResourceRef) string {
	return fmt.Sprintf("qc-%s-%s", trunc(ref.Resource, 20), trunc(ref.IdentityHash, 8))
}

// GrantName returns the deterministic QuotaGrant name in the provider
// workspace for one (governed ref, consumer) pair. The consumer segment is
// the first 16 hex characters of the sha256 of the consumer string, NOT a
// character substitution: kcp logical cluster paths (e.g. "root:c1") contain
// colons, and any lossy substitution (such as replacing ':' with '-') maps
// distinct consumers onto the same name — e.g. "root:a-b" and "root-a:b"
// would collide — letting one tenant's claim mutate another tenant's grant.
// Hashing is deterministic, bounded in length regardless of consumer path
// depth, and collision-safe for all practical purposes; the human-readable
// consumer identity is preserved separately in spec.consumer, which the
// reconciler must check before mutating an existing grant.
func GrantName(ref registry.ResourceRef, consumer string) string {
	sum := sha256.Sum256([]byte(consumer))
	consumerHash := hex.EncodeToString(sum[:])[:16]

	return fmt.Sprintf("qg-%s-%s-%s", trunc(ref.Resource, 20), trunc(ref.IdentityHash, 8), consumerHash)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}

	return s
}
