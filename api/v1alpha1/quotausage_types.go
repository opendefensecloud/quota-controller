// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// QuotaUsage is the internal, authoritative accounting object for one
// (consumerCluster, group, resource, identityHash). Not exported to providers or consumers.
type QuotaUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec   QuotaUsageSpec   `json:"spec"`
	Status QuotaUsageStatus `json:"status,omitempty"`
}

// QuotaUsageSpec records the identity this ledger belongs to (for humans/debugging;
// enforcement keys by object name). All fields are set once at creation.
type QuotaUsageSpec struct {
	Consumer     string `json:"consumer"`
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	IdentityHash string `json:"identityHash"`
}

type QuotaUsageStatus struct {
	// Confirmed is the real live object count, owned by the accounting reconciler.
	// +kubebuilder:validation:Minimum=0
	Confirmed int32 `json:"confirmed"`
	// Reservations are admits allowed but not yet observed as live objects.
	// +optional
	Reservations []Reservation `json:"reservations,omitempty"`
}

// Reservation is an in-flight admit slot with a TTL.
type Reservation struct {
	// Key identifies the reserved object: "namespace/name" for namespaced
	// objects, the bare "name" for cluster-scoped ones (quota.ObjectKey), or
	// "uid:<request UID>" for generateName requests whose final name is not
	// known at admission time.
	Key string `json:"key"`
	// ExpiresAt is when the sweep may reclaim this reservation if unfulfilled.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// +kubebuilder:object:root=true
type QuotaUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QuotaUsage `json:"items"`
}

func init() { SchemeBuilder.Register(&QuotaUsage{}, &QuotaUsageList{}) }
