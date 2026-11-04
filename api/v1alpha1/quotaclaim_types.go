// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ClaimPhase is the consumer-visible state of a request (spec §6.3).
// +kubebuilder:validation:Enum=None;Pending;Approved;Rejected
type ClaimPhase string

const (
	ClaimNone     ClaimPhase = "None"
	ClaimPending  ClaimPhase = "Pending"
	ClaimApproved ClaimPhase = "Approved"
	ClaimRejected ClaimPhase = "Rejected"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaClaim is the consumer-facing request+view object (spec §6.3). The
// controller pre-creates one per (consumer, governed resource); consumers may
// ONLY update spec.requestedLimit (enforced by the quota-consumer export's
// maximalPermissionPolicy, §10) and read status. Enforcement NEVER reads this
// type (ADR-002) — it only triggers the provider-gated approval path.
type QuotaClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              QuotaClaimSpec   `json:"spec"`
	Status            QuotaClaimStatus `json:"status,omitempty"`
}

type QuotaClaimSpec struct {
	// Governed is the identity tuple, stamped by the controller at pre-creation.
	Governed GovernedIdentity `json:"governed"`
	// RequestedLimit is the ONLY field consumers change. Omitted = no request,
	// the claim just shows the current effective limit.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RequestedLimit *int32 `json:"requestedLimit,omitempty"`
}

type QuotaClaimStatus struct {
	// +optional
	Phase ClaimPhase `json:"phase,omitempty"`
	// EffectiveLimit is what the webhook enforces right now (§9).
	// +optional
	EffectiveLimit *int32 `json:"effectiveLimit,omitempty"`
	// GrantedLimit is the approved override, if any.
	// +optional
	GrantedLimit *int32 `json:"grantedLimit,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// +kubebuilder:object:root=true
type QuotaClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaClaim `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaClaim{}, &QuotaClaimList{}) }
