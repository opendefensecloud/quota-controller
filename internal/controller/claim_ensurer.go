// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// ClaimEnsurer pre-creates (and garbage-collects) the per-consumer QuotaClaim
// so the current limit is always visible (spec §5.3 visibility, R11).
// Consumers cannot create claims themselves (maximalPermissionPolicy, §10),
// so this is the ONLY path by which a claim comes to exist.
type ClaimEnsurer struct {
	// ConsumerClientFor returns a client scoped to the consumer's logical
	// cluster (via the quota-consumer VW in production, plain client in tests).
	ConsumerClientFor func(ctx context.Context, consumerCluster string) (client.Client, error)
}

// Ensure creates the claim if absent, then is called again on every
// discovery sweep for every currently-bound consumer (idempotent): while the
// claim is still phase None, it refreshes status.effectiveLimit to the
// current policy default, so a defaultLimit change on the CQ becomes visible
// without waiting for a new request. It never touches an existing spec (the
// consumer's requestedLimit is theirs) and never touches a claim once it has
// moved off phase None (Pending/Approved/Rejected) — those are owned by the
// grant reconciler's path.
func (e *ClaimEnsurer) Ensure(ctx context.Context, consumerCluster string, ref registry.ResourceRef, defaultLimit int32) error {
	cl, err := e.ConsumerClientFor(ctx, consumerCluster)
	if err != nil {
		return fmt.Errorf("consumer client for %s: %w", consumerCluster, err)
	}

	name := selfservice.ClaimName(ref)
	existing := &v1alpha1.QuotaClaim{}
	err = cl.Get(ctx, types.NamespacedName{Name: name}, existing)

	switch {
	case apierrors.IsNotFound(err):
		claim := &v1alpha1.QuotaClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.QuotaClaimSpec{
				Governed: v1alpha1.GovernedIdentity{
					Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash,
				},
			},
		}
		if err := cl.Create(ctx, claim); err != nil {
			return fmt.Errorf("creating claim %s in %s: %w", name, consumerCluster, err)
		}
		claim.Status = v1alpha1.QuotaClaimStatus{
			Phase:          v1alpha1.ClaimNone,
			EffectiveLimit: &defaultLimit,
		}

		return cl.Status().Update(ctx, claim)

	case err != nil:
		return fmt.Errorf("getting claim %s in %s: %w", name, consumerCluster, err)
	}

	// Existing claim: only refresh the default-derived effective limit while no
	// decision applies (a granted claim's status is owned by the grant path).
	if existing.Status.Phase == "" || existing.Status.Phase == v1alpha1.ClaimNone {
		if existing.Status.EffectiveLimit == nil || *existing.Status.EffectiveLimit != defaultLimit {
			existing.Status.Phase = v1alpha1.ClaimNone
			existing.Status.EffectiveLimit = &defaultLimit

			return cl.Status().Update(ctx, existing)
		}
	}

	return nil
}

// Remove garbage-collects the claim when the (consumer, governed resource)
// pair disappears (unbind). Grants stay untouched — provider-owned records.
func (e *ClaimEnsurer) Remove(ctx context.Context, consumerCluster string, ref registry.ResourceRef) error {
	cl, err := e.ConsumerClientFor(ctx, consumerCluster)
	if err != nil {
		return fmt.Errorf("consumer client for %s: %w", consumerCluster, err)
	}

	claim := &v1alpha1.QuotaClaim{ObjectMeta: metav1.ObjectMeta{Name: selfservice.ClaimName(ref)}}
	if err := cl.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting claim in %s: %w", consumerCluster, err)
	}

	return nil
}
