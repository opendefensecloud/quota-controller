// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"strings"

	registrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testIdentityHash is a short hex string used as the fake identityHash in all
// webhook installer tests. It is chosen to fit within a single DNS label (≤63
// chars) so that webhook entry names are valid.
const testIdentityHash = "abc123def456"

// testCQ builds a ConsumptionQuota with status.identityHash pre-set so that
// the installer under test does not need to hit the API server for it.
//
//nolint:unparam // group and version are parameterised for clarity; callers currently all use the same value.
func testCQ(name, group, version, resource, identity string) *v1alpha1.ConsumptionQuota {
	return &v1alpha1.ConsumptionQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.ConsumptionQuotaSpec{
			Governed: v1alpha1.GovernedResource{
				APIExportName: "s3-export",
				Group:         group,
				Version:       version,
				Resource:      resource,
			},
			By:           "Count",
			DefaultLimit: 10,
		},
		Status: v1alpha1.ConsumptionQuotaStatus{
			IdentityHash: identity,
		},
	}
}

var _ = Describe("WebhookInstaller", func() {
	ctx := context.Background()

	// Clean up the VWC between specs to avoid cross-test pollution.
	AfterEach(func() {
		vwc := &registrationv1.ValidatingWebhookConfiguration{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc); err == nil {
			_ = k8sClient.Delete(ctx, vwc)
		}
	})

	Describe("Reconcile", func() {
		It("installs a ValidatingWebhookConfiguration named consumption-quota with one CREATE rule", func() {
			cq := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)

			installer := controller.NewWebhookInstaller(
				k8sClient,
				"https://webhook.example.com",
				[]byte("test-ca"),
			)

			Expect(installer.Reconcile(ctx, cq)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())

			Expect(vwc.Webhooks).To(HaveLen(1))

			wh := vwc.Webhooks[0]

			// Exactly one rule, operation CREATE.
			Expect(wh.Rules).To(HaveLen(1))
			Expect(wh.Rules[0].Operations).To(ConsistOf(registrationv1.OperationType("CREATE")))
			Expect(wh.Rules[0].APIGroups).To(ConsistOf("s3.example.com"))
			Expect(wh.Rules[0].Resources).To(ConsistOf("buckets"))

			// ClientConfig URL encodes the identity.
			Expect(wh.ClientConfig.URL).NotTo(BeNil())
			Expect(*wh.ClientConfig.URL).To(HaveSuffix("/validate/s3.example.com/buckets/" + testIdentityHash))

			// CA bundle preserved.
			Expect(wh.ClientConfig.CABundle).To(Equal([]byte("test-ca")))
		})

		It("is idempotent: a second Reconcile call updates not duplicates", func() {
			cq := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)

			Expect(installer.Reconcile(ctx, cq)).To(Succeed())
			Expect(installer.Reconcile(ctx, cq)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())
			Expect(vwc.Webhooks).To(HaveLen(1))
		})

		It("merges two ConsumptionQuotas governing different resources into one VWC with two entries", func() {
			cq1 := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)
			cq2 := testCQ("quota-s3-queues", "s3.example.com", "v1", "queues", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)

			Expect(installer.Reconcile(ctx, cq1)).To(Succeed())
			Expect(installer.Reconcile(ctx, cq2)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())
			Expect(vwc.Webhooks).To(HaveLen(2))

			// Both entries must target CREATE.
			for _, wh := range vwc.Webhooks {
				Expect(wh.Rules).To(HaveLen(1))
				Expect(wh.Rules[0].Operations).To(ConsistOf(registrationv1.OperationType("CREATE")))
			}

			// Collect resources from entries.
			resources := make([]string, 0, 2)
			for _, wh := range vwc.Webhooks {
				resources = append(resources, wh.Rules[0].Resources[0])
			}

			Expect(resources).To(ConsistOf("buckets", "queues"))
		})

		It("skips installation when identityHash is empty", func() {
			cq := testCQ("quota-no-identity", "s3.example.com", "v1", "buckets", "")

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(installer.Reconcile(ctx, cq)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)
			Expect(err).To(HaveOccurred()) // should not exist
		})
	})

	Describe("Remove", func() {
		It("deletes the VWC when the last contribution is removed", func() {
			cq := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)

			Expect(installer.Reconcile(ctx, cq)).To(Succeed())

			// Confirm VWC exists before removal.
			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())

			Expect(installer.Remove(ctx, cq)).To(Succeed())

			err := k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)
			Expect(err).To(HaveOccurred(), "VWC should be deleted after last contribution removed")
		})

		It("updates the VWC (removes only the relevant entry) when one of two CQs is removed", func() {
			cq1 := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)
			cq2 := testCQ("quota-s3-queues", "s3.example.com", "v1", "queues", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)

			Expect(installer.Reconcile(ctx, cq1)).To(Succeed())
			Expect(installer.Reconcile(ctx, cq2)).To(Succeed())

			Expect(installer.Remove(ctx, cq1)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())
			Expect(vwc.Webhooks).To(HaveLen(1))
			Expect(vwc.Webhooks[0].Rules[0].Resources).To(ConsistOf("queues"))

			// URL must still encode the remaining identity.
			Expect(*vwc.Webhooks[0].ClientConfig.URL).To(
				SatisfyAll(
					ContainSubstring("/validate/"),
					ContainSubstring("queues"),
					ContainSubstring(testIdentityHash),
				),
			)
		})

		It("is a no-op when called for a CQ that was never reconciled", func() {
			cq := testCQ("quota-unknown", "s3.example.com", "v1", "buckets", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(installer.Remove(ctx, cq)).To(Succeed())
		})
	})

	// Restart-resilience specs verify that a fresh installer (empty in-memory state,
	// simulating a process restart) reads the persisted VWC and preserves sibling
	// entries when reconciling or removing a single CQ.
	//
	// These specs would FAIL against the old contributions-map implementation because
	// Remove/Reconcile with an empty map would overwrite the VWC with only the
	// in-memory view, losing sibling entries.
	Describe("restart resilience", func() {
		It("Remove preserves sibling entries when the installer has no in-memory state", func() {
			cqA := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)
			cqB := testCQ("quota-s3-queues", "s3.example.com", "v1", "queues", testIdentityHash)

			// First installer populates the VWC with two entries.
			firstInstaller := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(firstInstaller.Reconcile(ctx, cqA)).To(Succeed())
			Expect(firstInstaller.Reconcile(ctx, cqB)).To(Succeed())

			// Simulate a process restart: fresh installer has no in-memory knowledge of CQ-B.
			freshInstaller := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(freshInstaller.Remove(ctx, cqA)).To(Succeed())

			// VWC must still exist and retain only CQ-B's entry.
			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed(),
				"VWC must survive removal of CQ-A even after a simulated restart")
			Expect(vwc.Webhooks).To(HaveLen(1))
			Expect(vwc.Webhooks[0].Rules[0].Resources).To(ConsistOf("queues"))
		})

		It("Reconcile preserves sibling entries when the installer has no in-memory state", func() {
			cqA := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)
			cqB := testCQ("quota-s3-queues", "s3.example.com", "v1", "queues", testIdentityHash)

			// First installer writes CQ-B's entry into the persisted VWC.
			firstInstaller := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(firstInstaller.Reconcile(ctx, cqB)).To(Succeed())

			// Simulate a process restart: fresh installer has no in-memory knowledge of CQ-B.
			freshInstaller := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(freshInstaller.Reconcile(ctx, cqA)).To(Succeed())

			// VWC must contain both entries.
			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())
			Expect(vwc.Webhooks).To(HaveLen(2))

			resources := make([]string, 0, 2)
			for _, wh := range vwc.Webhooks {
				resources = append(resources, wh.Rules[0].Resources[0])
			}
			Expect(resources).To(ConsistOf("buckets", "queues"))
		})
	})

	Describe("URL path encoding", func() {
		It("constructs the identity-scoped path /validate/<group>/<resource>/<identity>", func() {
			cq := testCQ("quota-s3-buckets", "s3.example.com", "v1", "buckets", testIdentityHash)

			installer := controller.NewWebhookInstaller(k8sClient, "https://webhook.example.com", nil)
			Expect(installer.Reconcile(ctx, cq)).To(Succeed())

			vwc := &registrationv1.ValidatingWebhookConfiguration{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "consumption-quota"}, vwc)).To(Succeed())

			url := *vwc.Webhooks[0].ClientConfig.URL
			Expect(strings.HasSuffix(url, "/validate/s3.example.com/buckets/"+testIdentityHash)).To(BeTrue(),
				"expected URL to end with /validate/s3.example.com/buckets/%s, got %s", testIdentityHash, url,
			)
		})
	})
})
