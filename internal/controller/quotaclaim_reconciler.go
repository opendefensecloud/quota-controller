// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// QuotaClaimReconciler turns consumer requests into provider-workspace grants
// (spec §8 steps 2–3). It reads ONLY the requestedLimit trigger from the claim
// — every authorization input (ceiling, default) comes from the provider-side
// PolicyIndex (ADR-002 invariant).
type QuotaClaimReconciler struct {
	// ProviderClientFor returns a client scoped to the provider's logical
	// cluster (via the quota-provider VW in production).
	ProviderClientFor func(ctx context.Context, providerCluster string) (client.Client, error)
	Policies          *selfservice.PolicyIndex
}

// Reconcile handles one claim event. claimCluster is the consumer's logical
// cluster; cl is a client scoped to it (the multicluster manager provides both).
func (r *QuotaClaimReconciler) Reconcile(ctx context.Context, claimCluster string, cl client.Client, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("claim", req.Name, "consumer", claimCluster)

	claim := &v1alpha1.QuotaClaim{}
	if err := cl.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !claim.DeletionTimestamp.IsZero() || claim.Spec.RequestedLimit == nil {
		return ctrl.Result{}, nil // nothing requested; claim is view-only
	}

	ref := registry.ResourceRef{
		Group:        claim.Spec.Governed.Group,
		Resource:     claim.Spec.Governed.Resource,
		IdentityHash: claim.Spec.Governed.IdentityHash,
	}

	// Identity ownership guard (I3): the claim's object name commits it to one
	// governed ref (selfservice.ClaimName(ref)) at creation time. A consumer
	// with update rights on the whole claim resource could otherwise retarget
	// spec.governed to a different (more favorable) ref — e.g. one with a
	// higher autoApproveCeiling — and have this reconciler spawn a grant for
	// it under the original claim's name. Refuse rather than risk spawning a
	// grant or writing status for a mismatched identity.
	if want := selfservice.ClaimName(ref); claim.Name != want {
		logger.Info("claim name does not match its governed identity; skipping (possible retargeted claim)",
			"wantName", want)

		return ctrl.Result{}, nil
	}

	policy, ok := r.Policies.Get(ref)
	if !ok {
		// Unknown governed ref: policy not synced (or claim is stale after an
		// identity rotation). Do NOT write a grant — requeue via next event.
		logger.Info("no policy for claim's governed ref; skipping")

		return ctrl.Result{}, nil
	}

	requested := *claim.Spec.RequestedLimit
	decision := selfservice.Decide(requested, policy.AutoApproveCeiling)

	pcl, err := r.ProviderClientFor(ctx, policy.ProviderCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("provider client for %s: %w", policy.ProviderCluster, err)
	}

	name := selfservice.GrantName(ref, claimCluster)
	existing := &v1alpha1.QuotaGrant{}
	err = pcl.Get(ctx, types.NamespacedName{Name: name}, existing)

	switch {
	case apierrors.IsNotFound(err):
		grant := &v1alpha1.QuotaGrant{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.QuotaGrantSpec{
				Consumer:    claimCluster,
				GovernedRef: policy.CQName,
				Governed: v1alpha1.GovernedIdentity{
					Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash,
				},
				RequestedLimit: &requested,
				Decision:       decision,
				Reason:         selfservice.ReasonFor(decision),
			},
		}
		if decision == v1alpha1.GrantApproved {
			grant.Spec.GrantedLimit = &requested
		}
		logger.Info("writing grant", "decision", decision, "requested", requested)

		return ctrl.Result{}, pcl.Create(ctx, grant)

	case err != nil:
		return ctrl.Result{}, fmt.Errorf("getting grant %s: %w", name, err)
	}

	if existing.Spec.Consumer != claimCluster {
		return ctrl.Result{}, fmt.Errorf("grant %s belongs to consumer %q, not %q (name collision); refusing to modify", name, existing.Spec.Consumer, claimCluster)
	}

	// Existing grant. Re-resolve the whole self-service state machine against the
	// new requestedLimit (re-open, escalation, down-scale — see
	// selfservice.ResolveExistingGrant), keeping the grant's ObjectMeta for the
	// update.
	updated := existing.DeepCopy()
	updated.Spec = selfservice.ResolveExistingGrant(existing.Spec, requested, policy.AutoApproveCeiling)
	if equalGrantSpecs(existing.Spec, updated.Spec) {
		return ctrl.Result{}, nil
	}
	logger.Info("updating grant", "decision", updated.Spec.Decision, "requested", requested)

	return ctrl.Result{}, pcl.Update(ctx, updated)
}

// equalGrantSpecs reports whether two grant specs are field-for-field equal. It
// uses apiequality.Semantic.DeepEqual (the same "did anything change?" gate as
// quota.Store) rather than a hand-listed field comparison so a new
// QuotaGrantSpec field can never silently fall out of the reconciler's
// update-skip check and cause a missed write. Pointer fields compare by pointee
// value.
func equalGrantSpecs(a, b v1alpha1.QuotaGrantSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}
