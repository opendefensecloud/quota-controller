// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
)

// cqLifecycle drives everything hung off a single ConsumptionQuota event on the
// provider manager: the CQ status/finalizer reconcile (ConsumptionQuotaReconciler
// + webhook installer), the per-export accounting informer lifecycle, and the
// self-service PolicyIndex (ADR-002). Because the VWC is installed in the SAME
// workspace as the ConsumptionQuota (unlike dep-ctrl's cross-workspace case), it
// builds a per-reconcile reconciler + installer bound to the reconcile
// workspace's own client.
type cqLifecycle struct {
	mgr            mcmanager.Manager
	baseCfg        *rest.Config
	scheme         *runtime.Scheme
	reg            *registry.Registry
	webhookBaseURL string
	caBundle       []byte
	acct           *accountingManager
	policies       *selfservice.PolicyIndex

	// cqPolicyRefs remembers the governed ref each ConsumptionQuota last fed into
	// the policy index. The deletion paths must Delete by ref, but by the time
	// they run the CQ object (and its stamped identity) is gone — this map is the
	// only place the ref survives. Keyed cluster/name, like the accounting
	// manager's cqRef.
	mu           sync.Mutex
	cqPolicyRefs map[string]registry.ResourceRef
}

func newCQLifecycle(
	mgr mcmanager.Manager,
	baseCfg *rest.Config,
	scheme *runtime.Scheme,
	reg *registry.Registry,
	webhookBaseURL string,
	caBundle []byte,
	acct *accountingManager,
	policies *selfservice.PolicyIndex,
) *cqLifecycle {
	return &cqLifecycle{
		mgr:            mgr,
		baseCfg:        baseCfg,
		scheme:         scheme,
		reg:            reg,
		webhookBaseURL: webhookBaseURL,
		caBundle:       caBundle,
		acct:           acct,
		policies:       policies,
		cqPolicyRefs:   map[string]registry.ResourceRef{},
	}
}

// Reconcile handles one ConsumptionQuota event: reconcile the CQ, then drive the
// accounting informer lifecycle and self-service policy off the same event.
func (c *cqLifecycle) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx).WithValues("cq", req.Name, "cluster", req.ClusterName)

	cl, err := c.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	pcClient := cl.GetClient()

	// Direct (non-VW) client scoped to this reconcile's provider workspace.
	// The quota-provider VW does not serve apiexports, so the governed service
	// APIExport must be read through a direct connection to the provider
	// logical cluster.
	directWSClient, err := kcp.WorkspaceClient(c.baseCfg, req.ClusterName.String(), c.scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	cqr := &controller.ConsumptionQuotaReconciler{Client: pcClient, APIExportReader: directWSClient, Reg: c.reg}
	if c.webhookBaseURL != "" {
		cqr.Installer = controller.NewWebhookInstaller(pcClient, c.webhookBaseURL, c.caBundle)
	}

	res, err := cqr.Reconcile(ctx, ctrl.Request{NamespacedName: req.NamespacedName})
	if err != nil {
		return res, err
	}

	// Drive the accounting informer lifecycle from the same event. The re-Get
	// deliberately observes the object AFTER Reconcile's finalizer/status
	// writes: NotFound can only be seen once Reconcile has removed the
	// finalizer (webhook teardown done), so acct.Remove never races a webhook
	// that is still installed, and a stamped identityHash is guaranteed
	// current because Reconcile wrote it before returning.
	cq := &v1alpha1.ConsumptionQuota{}
	cqRef := req.ClusterName.String() + "/" + req.Name
	if gErr := pcClient.Get(ctx, req.NamespacedName, cq); gErr != nil {
		if apierrors.IsNotFound(gErr) {
			c.acct.Remove(req.ClusterName.String(), req.Name)
			c.dropPolicy(cqRef)

			return res, nil
		}

		return res, gErr
	}
	if !cq.DeletionTimestamp.IsZero() {
		c.acct.Remove(req.ClusterName.String(), req.Name)
		c.dropPolicy(cqRef)

		return res, nil
	}
	if cq.Status.IdentityHash == "" {
		// Identity not yet resolved; the reconciler above requeued. Accounting
		// waits until the identity is stamped.
		return res, nil
	}
	if err := c.acct.Ensure(ctx, req.ClusterName.String(), cq); err != nil {
		logger.Error(err, "unable to ensure accounting informer")

		return res, err
	}

	// Identity is stamped and accounting is up: publish (or refresh) the
	// self-service policy for this governed ref.
	ref := registry.ResourceRef{
		Group:        cq.Spec.Governed.Group,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: cq.Status.IdentityHash,
	}
	c.setPolicy(cqRef, ref, selfservice.Policy{
		ProviderCluster:    req.ClusterName.String(),
		CQName:             cq.Name,
		DefaultLimit:       cq.Spec.DefaultLimit,
		AutoApproveCeiling: cq.Spec.AutoApproveCeiling,
	})

	return res, nil
}

// setPolicy publishes (or refreshes) the policy for cqRef's governed ref. If the
// CQ switched governed refs (spec change or identity rotation) the stale entry
// is dropped first, so old-identity claims stop being authorized.
func (c *cqLifecycle) setPolicy(cqRef string, ref registry.ResourceRef, p selfservice.Policy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.cqPolicyRefs[cqRef]; ok && old != ref {
		c.policies.Delete(old)
	}
	c.cqPolicyRefs[cqRef] = ref
	c.policies.Set(ref, p)
}

// dropPolicy removes cqRef's last-published policy (used on CQ delete/teardown).
func (c *cqLifecycle) dropPolicy(cqRef string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref, ok := c.cqPolicyRefs[cqRef]; ok {
		c.policies.Delete(ref)
		delete(c.cqPolicyRefs, cqRef)
	}
}
