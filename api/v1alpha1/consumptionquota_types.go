// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// ConsumptionQuota caps how many instances of a governed APIExport resource each
// consumer workspace may create. Created by a provider alongside its APIExport.
type ConsumptionQuota struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ConsumptionQuotaSpec   `json:"spec"`
	Status            ConsumptionQuotaStatus `json:"status,omitempty"`
}

type ConsumptionQuotaSpec struct {
	// Governed is the exported resource type this quota caps.
	Governed GovernedResource `json:"governed"`
	// By selects the accounting mode. Phase 1 supports only "Count".
	// +kubebuilder:validation:Enum=Count
	// +kubebuilder:default=Count
	By string `json:"by"`
	// DefaultLimit applies to every consumer workspace with no grant.
	// +kubebuilder:validation:Minimum=0
	DefaultLimit int32 `json:"defaultLimit"`
	// AutoApproveCeiling is reserved for Phase 2 (self-service) and ignored in Phase 1.
	// +optional
	AutoApproveCeiling *int32 `json:"autoApproveCeiling,omitempty"`
}

// GovernedResource identifies an exported resource type by its APIExport and GVR.
type GovernedResource struct {
	// APIExportName is the APIExport in the same workspace as this policy.
	// +kubebuilder:validation:MinLength=1
	APIExportName string `json:"apiExportName"`
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
}

type ConsumptionQuotaStatus struct {
	// IdentityHash is resolved by the controller from the governed APIExport's
	// status.identityHash. It disambiguates same-named resources from different providers.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type ConsumptionQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConsumptionQuota `json:"items"`
}

func init() { SchemeBuilder.Register(&ConsumptionQuota{}, &ConsumptionQuotaList{}) }
