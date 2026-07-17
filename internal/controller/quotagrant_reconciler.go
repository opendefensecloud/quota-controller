// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// QuotaGrantReconciler propagates grant decisions to the consumer-visible
// claim status (spec §8 step 5). Enforcement pickup is the webhook's grant
// watcher — this reconciler only owns visibility and grant status stamping.
type QuotaGrantReconciler struct {
	// ConsumerClientFor returns a client scoped to the consumer's logical
	// cluster (via the quota-consumer VW in production).
	ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error)
	// DefaultLimitFor resolves the policy default for a governed ref (backed by
	// the controller's registry mirror); used when no grant applies.
	DefaultLimitFor func(ref registry.ResourceRef) (int32, bool)
	Now             func() time.Time
}

// Reconcile handles one grant event; providerCl is scoped to the provider's
// logical cluster (supplied by the multicluster manager).
func (r *QuotaGrantReconciler) Reconcile(ctx context.Context, providerCl client.Client, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("grant", req.Name)

	grant := &v1alpha1.QuotaGrant{}
	if err := providerCl.Get(ctx, req.NamespacedName, grant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !grant.DeletionTimestamp.IsZero() {
		// Enforcement reverts immediately via the webhook's grant watcher (it
		// forgets the grant and falls back to the default limit). The claim's
		// decision status, however, is NOT refreshed here: the discovery sweep's
		// ensure-all pass (I2) only touches phase-None claims, so a claim left
		// in Approved/Rejected by a deleted grant stays stuck showing that stale
		// decision indefinitely. This is a known gap tracked for follow-up;
		// providers should prefer setting spec.decision=Rejected over deleting
		// the grant outright, which keeps the claim status accurate.
		return ctrl.Result{}, nil
	}

	ref := registry.ResourceRef{
		Group:        grant.Spec.Governed.Group,
		Resource:     grant.Spec.Governed.Resource,
		IdentityHash: grant.Spec.Governed.IdentityHash,
	}

	// Compute the consumer-visible view.
	status := v1alpha1.QuotaClaimStatus{
		Reason: grant.Spec.Reason,
	}
	now := metav1.NewTime(r.Now())
	status.LastTransitionTime = &now

	switch {
	case grant.Spec.Decision == v1alpha1.GrantApproved && grant.Spec.GrantedLimit == nil:
		// Incomplete approval: Decision is Approved but no limit was granted, so
		// the webhook keeps enforcing the policy default (fail-closed, ADR-003).
		// Show the claim as still Pending with a clear reason rather than a
		// misleading "Approved" whose effective limit never moved; overrides the
		// grant's own reason in case it was created directly (no claim pass).
		status.Phase = v1alpha1.ClaimPending
		status.Reason = selfservice.ReasonApprovedNoLimit
	case grant.Spec.Decision == v1alpha1.GrantApproved &&
		grant.Spec.RequestedLimit != nil && *grant.Spec.RequestedLimit > *grant.Spec.GrantedLimit:
		// Approved, but the consumer has since asked for more than was granted:
		// an outstanding escalation the provider must act on (the claim reconciler
		// keeps the grant Approved so the webhook never drops the held limit). Show
		// Pending so the consumer sees the raise is not yet in effect, while
		// keeping the already-granted limit as both granted and effective.
		status.Phase = v1alpha1.ClaimPending
		status.GrantedLimit = grant.Spec.GrantedLimit
		status.EffectiveLimit = grant.Spec.GrantedLimit
	case grant.Spec.Decision == v1alpha1.GrantApproved:
		status.Phase = v1alpha1.ClaimApproved
		status.GrantedLimit = grant.Spec.GrantedLimit
		status.EffectiveLimit = grant.Spec.GrantedLimit
	case grant.Spec.Decision == v1alpha1.GrantRejected:
		status.Phase = v1alpha1.ClaimRejected
	default:
		status.Phase = v1alpha1.ClaimPending
	}
	if status.EffectiveLimit == nil {
		if def, ok := r.DefaultLimitFor(ref); ok {
			status.EffectiveLimit = &def
		}
	}

	// Write the claim status in the consumer workspace.
	ccl, err := r.ConsumerClientFor(ctx, grant.Spec.Consumer)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("consumer client for %s: %w", grant.Spec.Consumer, err)
	}
	claim := &v1alpha1.QuotaClaim{}
	if err := ccl.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim); err != nil {
		// Claim not (yet) there — e.g. GC'd after unbind. Nothing to update.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if claim.Status.Phase != status.Phase ||
		!int32PtrEqual(claim.Status.EffectiveLimit, status.EffectiveLimit) ||
		!int32PtrEqual(claim.Status.GrantedLimit, status.GrantedLimit) ||
		claim.Status.Reason != status.Reason {
		claim.Status = status
		if err := ccl.Status().Update(ctx, claim); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating claim status in %s: %w", grant.Spec.Consumer, err)
		}
		logger.Info("claim status written", "consumer", grant.Spec.Consumer, "phase", status.Phase)
	}

	// Stamp the grant's own phase so the provider sees what needs their action —
	// the same effective state the consumer's claim shows above, not the raw
	// spec.decision. An Approved grant still awaiting the provider — an unresolved
	// raise (requestedLimit > grantedLimit) or no grantedLimit yet — surfaces as
	// Pending on both sides. spec.decision and grantedLimit (the only fields the
	// webhook reads) are untouched, so enforcement is unaffected.
	grantPhase := grant.Spec.Decision
	if grant.Spec.AwaitsProvider() {
		grantPhase = v1alpha1.GrantPending
	}
	if grant.Status.Phase != grantPhase {
		grant.Status.Phase = grantPhase
		grant.Status.AppliedAt = &now

		return ctrl.Result{}, providerCl.Status().Update(ctx, grant)
	}

	return ctrl.Result{}, nil
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}
