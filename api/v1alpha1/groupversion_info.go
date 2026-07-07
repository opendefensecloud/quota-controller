// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// +kubebuilder:object:generate=true
// +groupName=quota.opendefense.cloud
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "quota.opendefense.cloud", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion} //nolint:staticcheck // scheme.Builder is the canonical pattern for API packages; deprecation is advisory only
	AddToScheme   = SchemeBuilder.AddToScheme
)
