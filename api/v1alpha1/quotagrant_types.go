// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GovernedIdentity is the kcp identity tuple addressing one exported resource
// type (spec §6.2/§6.3). Stamped by the controller from the governed
// APIExport's status.identityHash — never trusted from consumer input.
type GovernedIdentity struct {
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
	// +kubebuilder:validation:MinLength=1
	IdentityHash string `json:"identityHash"`
}

// GrantDecision is the provider's (or auto-approval's) verdict on a request.
// +kubebuilder:validation:Enum=Pending;Approved;Rejected
type GrantDecision string

const (
	GrantPending  GrantDecision = "Pending"
	GrantApproved GrantDecision = "Approved"
	GrantRejected GrantDecision = "Rejected"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaGrant is a per-(consumer, governed resource) limit override living in
// the PROVIDER workspace (spec §6.2). The provider approves/rejects here; the
// request/approval controller writes it on auto-approve. Enforcement's only
// override source (§9).
type QuotaGrant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaGrantSpec   `json:"spec"`
	Status            QuotaGrantStatus `json:"status,omitempty"`
}

type QuotaGrantSpec struct {
	// Consumer is the consumer workspace's logical cluster name.
	// +kubebuilder:validation:MinLength=1
	Consumer string `json:"consumer"`
	// GovernedRef names the ConsumptionQuota this grant overrides (same workspace).
	// +kubebuilder:validation:MinLength=1
	GovernedRef string `json:"governedRef"`
	// Governed is the identity tuple, stamped by the controller.
	Governed GovernedIdentity `json:"governed"`
	// RequestedLimit mirrors the consumer's claim (informational for the provider).
	// +kubebuilder:validation:Minimum=0
	// +optional
	RequestedLimit *int32 `json:"requestedLimit,omitempty"`
	// GrantedLimit is the limit that takes effect when Decision is Approved.
	// +kubebuilder:validation:Minimum=0
	// +optional
	GrantedLimit *int32 `json:"grantedLimit,omitempty"`
	// +kubebuilder:default=Pending
	Decision GrantDecision `json:"decision"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

// MirrorsOverride reports whether this grant should contribute a per-consumer
// limit override to enforcement (spec §9). Only a live (non-deleting), Approved
// grant that carries a concrete GrantedLimit raises or holds a limit; Pending,
// Rejected, deleting, or approved-without-a-limit grants must not. It is the
// single source of the webhook mirrors' "should I hold this override?" gate.
func (g *QuotaGrant) MirrorsOverride() bool {
	return g.DeletionTimestamp.IsZero() &&
		g.Spec.Decision == GrantApproved &&
		g.Spec.GrantedLimit != nil
}

// AwaitsProvider reports whether an Approved grant still needs provider action
// before it is fully settled: either no GrantedLimit has been set yet, or the
// consumer's RequestedLimit exceeds the GrantedLimit (an unresolved raise).
// Such a grant surfaces as Pending on both the grant's and the claim's phase
// even though spec.decision stays Approved and the held GrantedLimit keeps
// enforcing. Non-Approved grants are never awaiting in this sense.
func (s QuotaGrantSpec) AwaitsProvider() bool {
	if s.Decision != GrantApproved {
		return false
	}

	return s.GrantedLimit == nil ||
		(s.RequestedLimit != nil && *s.RequestedLimit > *s.GrantedLimit)
}

type QuotaGrantStatus struct {
	// Phase is the grant's effective state from the provider's standpoint: the
	// decision the controller applied, except an Approved grant still awaiting the
	// provider — a pending raise (requestedLimit > grantedLimit) or Approved with
	// no grantedLimit — surfaces as Pending. Mirrors the consumer claim's phase.
	// +optional
	Phase GrantDecision `json:"phase,omitempty"`
	// AppliedAt is when the controller propagated the decision (registry + claim).
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaGrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaGrant `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaGrant{}, &QuotaGrantList{}) }
