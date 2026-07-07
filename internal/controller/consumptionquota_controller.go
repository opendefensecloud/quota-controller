// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package controller contains reconcilers for the quota-controller operator.
package controller

import (
	"context"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

const webhookFinalizer = "quota.opendefense.cloud/webhook"

// ConsumptionQuotaReconciler resolves the governed APIExport's identityHash,
// stamps it on ConsumptionQuota.status.identityHash, feeds the limit registry
// so the admission webhook can enforce quota, and installs a CREATE
// ValidatingWebhookConfiguration in the provider workspace.
type ConsumptionQuotaReconciler struct {
	// Client is the quota-provider virtual-workspace client. The VW serves only
	// consumptionquotas and validatingwebhookconfigurations, so it is used for the
	// ConsumptionQuota get/update/status and (via the Installer) the VWC install.
	client.Client
	// APIExportReader is a DIRECT workspace-scoped client for the provider
	// workspace. The quota-provider VW does NOT serve apis.kcp.io types
	// (apiexports, apiresourceschemas, apiexportendpointslices) — not even when
	// permission-claimed — so the governed service APIExport (status.identityHash
	// here) and its APIExportEndpointSlice (read by the accounting reconciler in
	// cmd/controller/accounting.go) must be read through a direct connection to the
	// provider logical cluster.
	//
	// DO NOT replace this with the VW Client or rely on permissionClaims: verified
	// on-cluster that the accounting reconciler then never starts, confirmed stays
	// 0, and quota enforcement BYPASSES once the reservation TTL expires. (ADR-004
	// captures the decision and the on-cluster failure mode.)
	APIExportReader client.Reader
	Reg             *registry.Registry
	Installer       *WebhookInstaller
}

// Reconcile implements reconcile.Reconciler:
//  1. On deletion (DeletionTimestamp set): remove webhook, delete registry entry,
//     remove finalizer.
//  2. Ensure finalizer is present.
//  3. Resolve identityHash from the governed APIExport; requeue if not yet set.
//  4. Stamp status.identityHash.
//  5. Feed the limit registry.
//  6. Install/update the CREATE webhook for the governed GVR.
func (r *ConsumptionQuotaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cq := &v1alpha1.ConsumptionQuota{}
	if err := r.Get(ctx, req.NamespacedName, cq); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// --- Deletion path ---
	if !cq.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.handleDeletion(ctx, cq)
	}

	// --- Ensure finalizer ---
	if !controllerutil.ContainsFinalizer(cq, webhookFinalizer) {
		controllerutil.AddFinalizer(cq, webhookFinalizer)
		if err := r.Update(ctx, cq); err != nil {
			return ctrl.Result{}, err
		}
		// Re-reconcile after the update so we get the fresh object.
		return ctrl.Result{}, nil
	}

	// --- Resolve identityHash ---
	// Read the governed service APIExport through the DIRECT provider-workspace
	// client: the quota-provider virtual workspace does not serve apiexports, so
	// reading it via r.Client would fail ("no matches for kind APIExport").
	ex := &apisv1alpha2.APIExport{}
	if err := r.APIExportReader.Get(ctx, client.ObjectKey{Name: cq.Spec.Governed.APIExportName}, ex); err != nil {
		return ctrl.Result{}, err
	}

	id := ex.Status.IdentityHash
	if id == "" {
		// Identity not yet assigned by kcp; requeue until it is.
		return ctrl.Result{Requeue: true}, nil
	}

	// --- Stamp status ---
	if cq.Status.IdentityHash != id {
		cq.Status.IdentityHash = id
		if err := r.Status().Update(ctx, cq); err != nil {
			return ctrl.Result{}, err
		}
	}

	// --- Feed the limit registry ---
	r.Reg.Set(registry.ResourceRef{
		Group:        cq.Spec.Governed.Group,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: id,
	}, cq.Spec.DefaultLimit)

	// --- Install / update the CREATE webhook ---
	if r.Installer != nil {
		if err := r.Installer.Reconcile(ctx, cq); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// handleDeletion removes the webhook contribution, cleans up the registry, and
// removes the finalizer so the object can be garbage-collected.
func (r *ConsumptionQuotaReconciler) handleDeletion(ctx context.Context, cq *v1alpha1.ConsumptionQuota) error {
	if r.Installer != nil {
		if err := r.Installer.Remove(ctx, cq); err != nil {
			return err
		}
	}

	r.Reg.Delete(registry.ResourceRef{
		Group:        cq.Spec.Governed.Group,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: cq.Status.IdentityHash,
	})

	if controllerutil.ContainsFinalizer(cq, webhookFinalizer) {
		controllerutil.RemoveFinalizer(cq, webhookFinalizer)

		if err := r.Update(ctx, cq); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager registers the reconciler with the controller-runtime manager.
func (r *ConsumptionQuotaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ConsumptionQuota{}).
		Complete(r)
}
