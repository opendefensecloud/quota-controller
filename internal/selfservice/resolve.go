// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package selfservice

import (
	"fmt"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
)

// Grant decision reason text (spec §8). These are the human-readable
// explanations the controller stamps onto grant.spec.reason (and the claim's
// status.reason mirrors them), kept here beside the decision logic that emits
// them so the wording and the rule stay in one place.
const (
	// ReasonAutoApproved is stamped when a request lands at or below the
	// provider's autoApproveCeiling and is auto-approved without provider action.
	ReasonAutoApproved = "auto-approved (requestedLimit <= autoApproveCeiling)"
	// ReasonAwaitingApproval is stamped when a request is above the ceiling (or no
	// ceiling is set) and must wait for a manual provider decision.
	ReasonAwaitingApproval = "awaiting provider approval (requestedLimit above autoApproveCeiling or no ceiling set)"
	// ReasonApprovedNoLimit explains an approval that has no enforceable effect:
	// the grant's Decision is Approved but spec.grantedLimit is unset, so the
	// webhook keeps enforcing the policy default (fail-closed, ADR-003). It is
	// surfaced on both the grant (spec.reason) and the claim (status.reason) so a
	// provider knows to set spec.grantedLimit and a consumer knows the requested
	// raise is not yet in effect. Replaces the misleading "approved at 0" text.
	ReasonApprovedNoLimit = "approved but spec.grantedLimit is not set; the policy default stays enforced until the provider grants a limit"
)

// ReasonFor returns the auto-approval reason text for a freshly Decide-d verdict
// (Approved vs. awaiting provider). Used on the create path and by the
// re-evaluation state machine below.
func ReasonFor(d v1alpha1.GrantDecision) string {
	if d == v1alpha1.GrantApproved {
		return ReasonAutoApproved
	}

	return ReasonAwaitingApproval
}

// ResolveExistingGrant computes the new spec for an existing grant when the
// consumer's requestedLimit changes (or is re-observed). It is the pure §8
// self-service state machine: Consumer/GovernedRef/Governed carry over
// unchanged, RequestedLimit is set to the new ask, and Decision/GrantedLimit/
// Reason follow the rules below. A provider's manual verdict is sticky only to
// the exact request it was made against (existing.RequestedLimit records that
// value; provider edits never touch it — only this function does, in lockstep
// with the decision):
//
//   - A Rejected grant whose ask is unchanged keeps its rejection (the consumer
//     cannot re-spam the identical denied request).
//   - A changed ask re-runs the auto-approve rule against ceiling: within it
//     (re-)auto-approves; a re-opened rejection above it returns to Pending
//     (dropping any stale granted limit).
//   - The enforced limit is never RAISED without approval: an over-ceiling
//     escalation on an Approved grant holds the approved grantedLimit and only
//     records the pending raise in Reason. But the consumer MAY voluntarily
//     lower below the grant (self-service down-scale), which reduces
//     grantedLimit to the lower ask.
//
// The function only reassigns pointer fields (never mutates through the
// caller's pointers), so passing existing by value is safe.
func ResolveExistingGrant(existing v1alpha1.QuotaGrantSpec, requested int32, ceiling *int32) v1alpha1.QuotaGrantSpec {
	decision := Decide(requested, ceiling)

	updated := existing
	updated.RequestedLimit = &requested
	requestChanged := existing.RequestedLimit == nil || *existing.RequestedLimit != requested

	switch {
	case existing.Decision == v1alpha1.GrantRejected && !requestChanged:
		// Identical ask after a rejection: the provider's No stands. No change.
	case decision == v1alpha1.GrantApproved:
		// At/below the ceiling. Reached only when either the grant is not under a
		// standing rejection, or the consumer changed a rejected ask (the sticky
		// case above already caught an unchanged rejection). (Re-)auto-approve.
		updated.Decision = v1alpha1.GrantApproved
		updated.GrantedLimit = &requested
		updated.Reason = ReasonAutoApproved
	case existing.Decision == v1alpha1.GrantRejected:
		// Changed ask above the ceiling after a rejection: re-open for a fresh
		// provider decision (drop the stale granted limit, if any).
		updated.Decision = v1alpha1.GrantPending
		updated.GrantedLimit = nil
		updated.Reason = ReasonAwaitingApproval
	case existing.Decision == v1alpha1.GrantApproved:
		// decision == Pending on an already-approved grant (request over the
		// ceiling, or no ceiling). The provider's standing approval governs: a
		// request above the granted limit is an escalation the provider must act
		// on (grantedLimit held, raise surfaced in Reason); a request at or below
		// it stays approved, and if strictly below it self-service down-scales
		// grantedLimit to the lower ask.
		switch {
		case existing.GrantedLimit == nil:
			// Approved with no granted limit is not an escalation — it is an
			// incomplete approval. Say so plainly instead of "approved at 0".
			updated.Reason = ReasonApprovedNoLimit
		case requested > *existing.GrantedLimit:
			// Genuine over-grant escalation awaiting provider action.
			updated.Reason = fmt.Sprintf("approved at %d; escalation to %d awaits provider action", *existing.GrantedLimit, requested)
		default:
			// requested <= granted: the standing approval already covers the ask
			// (no escalation). If the consumer lowered strictly below the grant,
			// this is a self-service down-scale — reduce grantedLimit to the lower
			// ask so the webhook enforces it (always safe: never above what the
			// provider approved, and no new decision needed). requested == granted
			// collapses to a no-op (same value written back).
			updated.GrantedLimit = &requested
			updated.Reason = fmt.Sprintf("approved at %d", requested)
		}
	}

	return updated
}
