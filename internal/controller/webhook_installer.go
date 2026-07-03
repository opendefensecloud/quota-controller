// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	registrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
)

const quotaWebhookName = "consumption-quota"

// WebhookInstaller creates and updates a single ValidatingWebhookConfiguration
// named "consumption-quota" in the provider workspace so that kcp's admission
// plugin intercepts CREATE requests for the governed resource and routes them to
// the quota-controller's webhook server.
//
// Multiple ConsumptionQuotas may govern different resources in the same provider
// workspace. The installer merges them into one webhook entry per
// (group, version, resource, identityHash) combination, each with its own
// identity-scoped client-config path so the shared webhook server can route to
// the right per-identity CreationValidator.
//
// Client must be pre-wired to the provider workspace (via VW-routing in Task 10).
// In tests a plain envtest client suffices.
//
// The persisted ValidatingWebhookConfiguration is the sole source of truth for
// which entries are active. The installer uses read-modify-write semantics so
// that a process restart does not lose sibling entries: Reconcile upserts only
// this CQ's entry, and Remove filters out only this CQ's entry, preserving all
// others.
//
// Note: correct multi-replica operation assumes leader election so that only one
// replica performs reconcile/remove at a time.
type WebhookInstaller struct {
	// Client is the Kubernetes client for the provider workspace.
	Client client.Client
	// WebhookBaseURL is the base URL of the quota webhook server, e.g.
	// "https://webhook.example.com". The installer appends the identity-scoped
	// path "/validate/<group>/<resource>/<identityHash>" per entry.
	WebhookBaseURL string
	CABundle       []byte
}

// webhookRuleKey is the merge key for a single webhook entry in the VWC.
// It is unique per (governed GVR + identity), so two ConsumptionQuotas with
// identical spec collapse into one entry (deduplication).
type webhookRuleKey struct {
	Group        string
	Version      string
	Resource     string
	IdentityHash string
}

// NewWebhookInstaller returns an initialised installer.
func NewWebhookInstaller(c client.Client, webhookBaseURL string, caBundle []byte) *WebhookInstaller {
	return &WebhookInstaller{
		Client:         c,
		WebhookBaseURL: webhookBaseURL,
		CABundle:       caBundle,
	}
}

// Reconcile installs or updates the consumption-quota VWC to include a CREATE
// rule for the governed resource of cq.
// If cq.Status.IdentityHash is empty (identity not yet resolved) the call is a
// no-op; the caller is expected to requeue.
//
// Uses read-modify-write against the persisted VWC via CreateOrUpdate: this CQ's
// entry is upserted while all other (foreign) entries are preserved. On a write
// conflict the error is returned so controller-runtime requeues and retries.
func (w *WebhookInstaller) Reconcile(ctx context.Context, cq *v1alpha1.ConsumptionQuota) error {
	if cq.Status.IdentityHash == "" {
		return nil
	}

	logger := log.FromContext(ctx)

	key := webhookRuleKey{
		Group:        cq.Spec.Governed.Group,
		Version:      cq.Spec.Governed.Version,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: cq.Status.IdentityHash,
	}
	entryName := webhookEntryName(key)
	newEntry := w.buildWebhookEntry(key)

	vwc := &registrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: quotaWebhookName,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, w.Client, vwc, func() error {
		// Upsert: replace the existing entry with this name, or append if absent.
		// All other (foreign) entries are preserved, ensuring restart-resilience.
		for i, wh := range vwc.Webhooks {
			if wh.Name == entryName {
				vwc.Webhooks[i] = newEntry
				return nil
			}
		}
		vwc.Webhooks = append(vwc.Webhooks, newEntry)

		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting VWC entry %s: %w", entryName, err)
	}

	logger.Info("consumption-quota webhook entry upserted", "entry", entryName, "result", result)

	return nil
}

// Remove removes the VWC entry for cq. If no entries remain after removal the
// VWC is deleted entirely; otherwise the VWC is updated with the remaining
// entries.
//
// Uses read-modify-write against the persisted VWC: only this CQ's entry is
// filtered out, and all other (foreign) entries are preserved. On a write
// conflict the error is returned so controller-runtime requeues and retries.
func (w *WebhookInstaller) Remove(ctx context.Context, cq *v1alpha1.ConsumptionQuota) error {
	key := webhookRuleKey{
		Group:        cq.Spec.Governed.Group,
		Version:      cq.Spec.Governed.Version,
		Resource:     cq.Spec.Governed.Resource,
		IdentityHash: cq.Status.IdentityHash,
	}
	entryName := webhookEntryName(key)

	logger := log.FromContext(ctx)

	existing := &registrationv1.ValidatingWebhookConfiguration{}
	if err := w.Client.Get(ctx, types.NamespacedName{Name: quotaWebhookName}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("getting VWC %s: %w", quotaWebhookName, err)
	}

	// Filter out only this CQ's entry; preserve all foreign entries.
	filtered := make([]registrationv1.ValidatingWebhook, 0, len(existing.Webhooks))
	for _, wh := range existing.Webhooks {
		if wh.Name != entryName {
			filtered = append(filtered, wh)
		}
	}

	if len(filtered) == 0 {
		logger.Info("deleting consumption-quota webhook: no entries remain")
		if err := w.Client.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting VWC: %w", err)
		}

		return nil
	}

	existing.Webhooks = filtered
	logger.Info("updating consumption-quota webhook", "entries", len(filtered))
	if err := w.Client.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating VWC: %w", err)
	}

	return nil
}

// buildWebhookEntry constructs a single ValidatingWebhook entry for the given key.
// The entry carries a single CREATE rule, failurePolicy Fail, sideEffects None,
// and an identity-scoped client-config URL.
func (w *WebhookInstaller) buildWebhookEntry(key webhookRuleKey) registrationv1.ValidatingWebhook {
	failPolicy := registrationv1.Fail
	sideEffects := registrationv1.SideEffectClassNone
	timeout := int32(10)
	u := w.webhookURL(key)

	return registrationv1.ValidatingWebhook{
		Name:                    webhookEntryName(key),
		AdmissionReviewVersions: []string{"v1"},
		ClientConfig: registrationv1.WebhookClientConfig{
			URL:      &u,
			CABundle: w.CABundle,
		},
		FailurePolicy:  &failPolicy,
		SideEffects:    &sideEffects,
		TimeoutSeconds: &timeout,
		Rules: []registrationv1.RuleWithOperations{{
			Operations: []registrationv1.OperationType{registrationv1.Create},
			Rule: registrationv1.Rule{
				APIGroups:   []string{key.Group},
				APIVersions: []string{key.Version},
				Resources:   []string{key.Resource},
			},
		}},
	}
}

// webhookURL builds the full URL for a given rule key:
// <WebhookBaseURL>/validate/<group>/<resource>/<identityHash>.
func (w *WebhookInstaller) webhookURL(key webhookRuleKey) string {
	return fmt.Sprintf("%s/validate/%s/%s/%s",
		w.WebhookBaseURL, key.Group, key.Resource, key.IdentityHash)
}

// webhookEntryName returns a valid qualified DNS name for a ValidatingWebhook
// entry, unique per (resource, identityHash). The identity hash is truncated to
// 32 characters (enough for uniqueness) to stay within DNS label length limits.
func webhookEntryName(key webhookRuleKey) string {
	id := key.IdentityHash
	if len(id) > 32 {
		id = id[:32]
	}

	return fmt.Sprintf("%s-%s.quota.opendefense.cloud", key.Resource, id)
}
