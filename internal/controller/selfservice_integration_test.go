// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// In envtest there is a single API server, so "provider workspace" and
// "consumer workspace" collapse onto the same client. The factories return
// k8sClient; the workspace routing itself is exercised in the e2e suite.
func sameCluster(_ context.Context, _ string) (client.Client, error) { return k8sClient, nil }

var _ = Describe("self-service reconcilers", func() {
	var (
		ref     registry.ResourceRef
		ensurer *controller.ClaimEnsurer
	)

	BeforeEach(func() {
		ref = registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "abc123def456"}
		ensurer = &controller.ClaimEnsurer{ConsumerClientFor: sameCluster}
	})

	AfterEach(func() {
		_ = k8sClient.DeleteAllOf(ctx, &v1alpha1.QuotaClaim{})
		_ = k8sClient.DeleteAllOf(ctx, &v1alpha1.QuotaGrant{})
	})

	Describe("ClaimEnsurer", func() {
		It("pre-creates a claim with stamped identity and default effective limit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Spec.Governed.IdentityHash).To(Equal("abc123def456"))
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(3))))
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimNone))
		})

		It("is idempotent and preserves an existing requestedLimit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Spec.RequestedLimit).To(HaveValue(Equal(int32(8))))
		})

		It("Remove deletes the pre-created claim", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			Expect(ensurer.Remove(ctx, "root:c1", ref)).To(Succeed())

			err := k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, &v1alpha1.QuotaClaim{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("QuotaClaimReconciler", func() {
		newReconciler := func(ceiling *int32) *controller.QuotaClaimReconciler {
			idx := selfservice.NewPolicyIndex()
			idx.Set(ref, selfservice.Policy{
				ProviderCluster: "root:p1", CQName: "bucket-quota",
				DefaultLimit: 3, AutoApproveCeiling: ceiling,
			})

			return &controller.QuotaClaimReconciler{ProviderClientFor: sameCluster, Policies: idx}
		}

		reconcileClaim := func(r *controller.QuotaClaimReconciler) {
			_, err := r.Reconcile(ctx, "root:c1", k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.ClaimName(ref)}})
			Expect(err).NotTo(HaveOccurred())
		}

		It("writes an Approved grant for a request at/below the ceiling", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10))))

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(8))))
			Expect(grant.Spec.Consumer).To(Equal("root:c1"))
			Expect(grant.Spec.GovernedRef).To(Equal("bucket-quota"))
		})

		It("writes a Pending grant above the ceiling and never sets grantedLimit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(99))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10))))

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantPending))
			Expect(grant.Spec.GrantedLimit).To(BeNil())
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(99))))
		})

		It("does not downgrade an existing Approved grant on a repeat identical request", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			r := newReconciler(new(int32(10)))
			reconcileClaim(r)
			reconcileClaim(r) // second pass must be a no-op

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
		})

		It("keeps an Approved grant on an over-ceiling re-request but surfaces the pending escalation in Reason (I4)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			r := newReconciler(new(int32(10)))
			reconcileClaim(r) // requested=8 <= ceiling=10 -> Approved

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(8))))

			// Re-request above the ceiling: decision must stay Approved at the
			// previously-granted limit, but Reason must explain the pending
			// escalation so providers scanning grants can spot it.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(99))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())
			reconcileClaim(r)

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved), "never silently escalate above what the provider approved")
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(8))), "grantedLimit stays at the provider-approved value")
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(99))), "requestedLimit mirrors the new ask")
			Expect(grant.Spec.Reason).To(Equal("approved at 8; escalation to 99 awaits provider action"))
		})

		It("ignores claims whose governed ref has no known policy (fail-closed: no grant)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			empty := &controller.QuotaClaimReconciler{ProviderClientFor: sameCluster, Policies: selfservice.NewPolicyIndex()}
			reconcileClaim(empty)

			err := k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, &v1alpha1.QuotaGrant{})
			Expect(err).To(HaveOccurred())
		})

		It("skips a claim whose object name does not match its governed identity (retargeted claim, I3)", func() {
			// refB is a distinct governed ref with its own registered policy. A
			// consumer with update rights on the whole claim resource could
			// retarget spec.governed away from what the object's name commits it
			// to (selfservice.ClaimName(ref)) — e.g. to point at a governed
			// resource with a more generous policy. The reconciler must refuse to
			// act on such a claim rather than spawn a grant for either identity.
			refB := registry.ResourceRef{Group: "s3.example.com", Resource: "objects", IdentityHash: "999888777666"}
			idx := selfservice.NewPolicyIndex()
			idx.Set(ref, selfservice.Policy{ProviderCluster: "root:p1", CQName: "bucket-quota", DefaultLimit: 3, AutoApproveCeiling: new(int32(10))})
			idx.Set(refB, selfservice.Policy{ProviderCluster: "root:p1", CQName: "object-quota", DefaultLimit: 3, AutoApproveCeiling: new(int32(10))})

			retargeted := &v1alpha1.QuotaClaim{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.ClaimName(ref)}, // name still commits to ref (A)
				Spec: v1alpha1.QuotaClaimSpec{
					Governed:       v1alpha1.GovernedIdentity{Group: refB.Group, Resource: refB.Resource, IdentityHash: refB.IdentityHash}, // but spec now points at refB
					RequestedLimit: new(int32(8)),
				},
			}
			Expect(k8sClient.Create(ctx, retargeted)).To(Succeed())

			r := &controller.QuotaClaimReconciler{ProviderClientFor: sameCluster, Policies: idx}
			_, err := r.Reconcile(ctx, "root:c1", k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.ClaimName(ref)}})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, &v1alpha1.QuotaGrant{})
			Expect(err).To(HaveOccurred(), "no grant for the name-committed ref")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(refB, "root:c1")}, &v1alpha1.QuotaGrant{})
			Expect(err).To(HaveOccurred(), "no grant for the spec-retargeted ref either")
		})

		It("refuses to mutate a grant whose name collides but whose spec.consumer differs (confused deputy)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(8))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			// Pre-create a grant AT root:c1's name, but owned by a different consumer
			// (simulating a name collision from a lossy encoding upstream).
			foreignGrant := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer:       "root:other",
					GovernedRef:    "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(2)), GrantedLimit: new(int32(2)), Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, foreignGrant)).To(Succeed())

			r := newReconciler(new(int32(10)))
			_, err := r.Reconcile(ctx, "root:c1", k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.ClaimName(ref)}})
			Expect(err).To(MatchError(ContainSubstring("name collision")))

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Consumer).To(Equal("root:other"))
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(2))))
		})

		It("surfaces an incomplete approval (Approved grant, no grantedLimit) with a clear reason, not 'approved at 0' (Bug B)", func() {
			// No ceiling: every request resolves to Pending, so approval is
			// manual. Simulate a provider who set Decision=Approved but forgot
			// spec.grantedLimit — the exact live trap from quota-test-1.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(6))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			manual := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: nil, Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, manual)).To(Succeed())

			reconcileClaim(newReconciler(nil)) // no ceiling

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved), "provider decision preserved")
			Expect(grant.Spec.GrantedLimit).To(BeNil(), "never fabricate a granted limit (fail-closed)")
			Expect(grant.Spec.Reason).To(Equal(selfservice.ReasonApprovedNoLimit))
			Expect(grant.Spec.Reason).NotTo(ContainSubstring("approved at 0"))
		})

		It("reports a plain 'approved at N' (no phantom escalation) when the standing grant already covers the request (I4, no ceiling)", func() {
			// No ceiling means every re-request is Pending, so the I4 branch
			// fires even when requested <= granted. It must not claim an
			// escalation is pending when the grant already covers the ask.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(6))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			covered := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6)), Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, covered)).To(Succeed())

			reconcileClaim(newReconciler(nil)) // no ceiling -> Pending decision -> I4

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(6))))
			Expect(grant.Spec.Reason).To(Equal("approved at 6"))
			Expect(grant.Spec.Reason).NotTo(ContainSubstring("escalation"))
		})

		It("re-opens a Rejected grant to Pending when the consumer changes the ask above the ceiling", func() {
			// A provider's rejection is sticky only to the exact request it was
			// made against (stored in grant.spec.requestedLimit). Changing the ask
			// re-runs the auto-approve rule; above the ceiling that means Pending.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(99))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			rejected := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: nil, Decision: v1alpha1.GrantRejected,
					Reason: "exceeds team budget",
				},
			}
			Expect(k8sClient.Create(ctx, rejected)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10)))) // 99 > ceiling -> Pending

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantPending), "a changed ask re-opens the rejection")
			Expect(grant.Spec.GrantedLimit).To(BeNil())
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(99))))
			Expect(grant.Spec.Reason).To(ContainSubstring("awaiting provider approval"))
		})

		It("re-approves a Rejected grant when the consumer's changed ask is within the ceiling", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(5))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			rejected := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(99)), GrantedLimit: nil, Decision: v1alpha1.GrantRejected,
				},
			}
			Expect(k8sClient.Create(ctx, rejected)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10)))) // 5 <= ceiling -> auto-approve

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved), "a changed ask within the ceiling auto-approves")
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(5))))
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(5))))
		})

		It("keeps a Rejected grant Rejected when the consumer re-submits the identical ask", func() {
			// Same value the provider already said No to (even though it is within
			// the ceiling): the manual verdict stands until the consumer actually
			// changes their ask, so a consumer cannot re-spam the identical request.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(6))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			rejected := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: nil, Decision: v1alpha1.GrantRejected,
					Reason: "exceeds team budget",
				},
			}
			Expect(k8sClient.Create(ctx, rejected)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10)))) // 6 <= ceiling, but unchanged -> stays Rejected

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantRejected), "the identical rejected ask must not auto-reopen")
			Expect(grant.Spec.Reason).To(Equal("exceeds team budget"))
		})

		It("down-scales an Approved grant when the consumer lowers the request below the grant (no ceiling)", func() {
			// Self-service down-scale: the consumer voluntarily reduces below what
			// the provider approved. It is always safe (never rises above the
			// grant), so grantedLimit follows the lower ask with no new decision.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(5))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			approved := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6)), Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, approved)).To(Succeed())

			reconcileClaim(newReconciler(nil)) // no ceiling -> Pending decision -> down-scale path

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved), "a reduction needs no new decision")
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(5))), "grantedLimit follows the consumer's lower request")
			Expect(grant.Spec.RequestedLimit).To(HaveValue(Equal(int32(5))))
			Expect(grant.Spec.Reason).To(Equal("approved at 5"))
		})

		It("down-scales an Approved grant when the consumer lowers the request within the ceiling", func() {
			// Same outcome via the auto-approve path, proving down-scale is
			// consistent across approval modes.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(5))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			approved := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(8)), GrantedLimit: new(int32(8)), Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, approved)).To(Succeed())

			reconcileClaim(newReconciler(new(int32(10)))) // 5 <= ceiling -> auto path down-scales

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(5))))
		})

		It("is idempotent after a down-scale (no thrash on the next reconcile)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			claim.Spec.RequestedLimit = new(int32(5))
			Expect(k8sClient.Update(ctx, claim)).To(Succeed())

			approved := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(6)), GrantedLimit: new(int32(6)), Decision: v1alpha1.GrantApproved,
				},
			}
			Expect(k8sClient.Create(ctx, approved)).To(Succeed())

			r := newReconciler(nil)
			reconcileClaim(r)
			reconcileClaim(r) // second pass must be a no-op

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(5))))
			Expect(grant.Spec.Reason).To(Equal("approved at 5"))
		})
	})

	Describe("QuotaGrantReconciler", func() {
		fixedNow := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

		writeGrant := func(decision v1alpha1.GrantDecision, granted *int32) {
			grant := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(8)), GrantedLimit: granted, Decision: decision,
				},
			}
			Expect(k8sClient.Create(ctx, grant)).To(Succeed())
		}

		reconcileGrant := func() {
			r := &controller.QuotaGrantReconciler{
				ConsumerClientFor: sameCluster,
				DefaultLimitFor:   func(_ registry.ResourceRef) (int32, bool) { return 3, true },
				Now:               func() time.Time { return fixedNow },
			}
			_, err := r.Reconcile(ctx, k8sClient,
				ctrl.Request{NamespacedName: types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}})
			Expect(err).NotTo(HaveOccurred())
		}

		It("writes Approved decisions back to the claim status", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, new(int32(8)))

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimApproved))
			Expect(claim.Status.GrantedLimit).To(HaveValue(Equal(int32(8))))
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(8))))
			Expect(claim.Status.LastTransitionTime.Time).To(BeTemporally("==", fixedNow))
		})

		It("writes Rejected decisions with the default as effective limit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantRejected, nil)

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimRejected))
			Expect(claim.Status.GrantedLimit).To(BeNil())
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(3))))
		})

		It("stamps grant status.phase and appliedAt", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, new(int32(8)))

			reconcileGrant()

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Status.Phase).To(Equal(v1alpha1.GrantApproved))
			Expect(grant.Status.AppliedAt.Time).To(BeTemporally("==", fixedNow))
		})

		It("surfaces an incomplete approval on the claim (Pending + clear reason, default effective) when grantedLimit is unset (Bug B)", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, nil) // Approved, but no granted limit

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimPending), "not Approved: nothing was actually granted")
			Expect(claim.Status.GrantedLimit).To(BeNil())
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(3))), "policy default stays enforced (fail-closed)")
			Expect(claim.Status.Reason).To(Equal(selfservice.ReasonApprovedNoLimit))
		})

		It("shows the claim as Pending on an over-grant escalation but keeps the granted limit enforced", func() {
			// Approved at 8, but the consumer has since asked for 99 (an over-ceiling
			// escalation the claim reconciler recorded on the still-Approved grant).
			// The consumer must see Pending (the raise is not yet in effect) while
			// still holding their granted 8 — never dropped back to the default.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			escalation := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(99)), GrantedLimit: new(int32(8)), Decision: v1alpha1.GrantApproved,
					Reason: "approved at 8; escalation to 99 awaits provider action",
				},
			}
			Expect(k8sClient.Create(ctx, escalation)).To(Succeed())

			reconcileGrant()

			claim := &v1alpha1.QuotaClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.ClaimName(ref)}, claim)).To(Succeed())
			Expect(claim.Status.Phase).To(Equal(v1alpha1.ClaimPending), "an outstanding raise shows as Pending")
			Expect(claim.Status.EffectiveLimit).To(HaveValue(Equal(int32(8))), "the already-granted limit stays enforced")
			Expect(claim.Status.GrantedLimit).To(HaveValue(Equal(int32(8))))
			Expect(claim.Status.Reason).To(Equal("approved at 8; escalation to 99 awaits provider action"))
		})

		It("surfaces an unresolved raise on the grant's OWN phase as Pending (provider signal)", func() {
			// The provider looks at the grant. An Approved grant with a pending raise
			// (requested > granted) must read Pending on its own status.phase so the
			// provider sees action is needed — symmetric with the consumer's claim —
			// while spec.decision/grantedLimit (what the webhook enforces) stay put.
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())

			escalation := &v1alpha1.QuotaGrant{
				ObjectMeta: metav1.ObjectMeta{Name: selfservice.GrantName(ref, "root:c1")},
				Spec: v1alpha1.QuotaGrantSpec{
					Consumer: "root:c1", GovernedRef: "bucket-quota",
					Governed:       v1alpha1.GovernedIdentity{Group: ref.Group, Resource: ref.Resource, IdentityHash: ref.IdentityHash},
					RequestedLimit: new(int32(10)), GrantedLimit: new(int32(6)), Decision: v1alpha1.GrantApproved,
					Reason: "approved at 6; escalation to 10 awaits provider action",
				},
			}
			Expect(k8sClient.Create(ctx, escalation)).To(Succeed())

			reconcileGrant()

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Status.Phase).To(Equal(v1alpha1.GrantPending), "a pending raise needs provider action -> Pending")
			Expect(grant.Status.AppliedAt).NotTo(BeNil())
			Expect(grant.Spec.Decision).To(Equal(v1alpha1.GrantApproved), "spec.decision (enforcement) untouched")
			Expect(grant.Spec.GrantedLimit).To(HaveValue(Equal(int32(6))), "grantedLimit (enforcement) untouched")
		})

		It("shows the grant's own phase as Pending for an Approved grant with no granted limit", func() {
			Expect(ensurer.Ensure(ctx, "root:c1", ref, 3)).To(Succeed())
			writeGrant(v1alpha1.GrantApproved, nil) // Approved but nothing actually granted yet

			reconcileGrant()

			grant := &v1alpha1.QuotaGrant{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: selfservice.GrantName(ref, "root:c1")}, grant)).To(Succeed())
			Expect(grant.Status.Phase).To(Equal(v1alpha1.GrantPending), "approved-with-no-limit still needs the provider to set grantedLimit")
		})
	})
})
