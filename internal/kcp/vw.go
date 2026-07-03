// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package kcp provides helpers for interacting with kcp-specific API objects.
package kcp

import (
	"context"
	"fmt"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VirtualWorkspaceURL returns the first virtual-workspace URL advertised by the
// APIExportEndpointSlice whose spec.export.name matches apiExportName.
//
// Adapted from dependency-controller/internal/kcp/endpointslice.go: instead of
// returning the full slice, we walk the status endpoints and hand back the URL
// string directly. The field layout (Spec.APIExport.Name, Status.APIExportEndpoints[].URL)
// is identical in kcp SDK v0.32.x.
func VirtualWorkspaceURL(ctx context.Context, c client.Client, apiExportName string) (string, error) {
	var list apisv1alpha1.APIExportEndpointSliceList
	if err := c.List(ctx, &list); err != nil {
		return "", fmt.Errorf("listing APIExportEndpointSlices: %w", err)
	}

	for i := range list.Items {
		if list.Items[i].Spec.APIExport.Name != apiExportName {
			continue
		}
		eps := list.Items[i].Status.APIExportEndpoints
		if len(eps) == 0 {
			return "", fmt.Errorf("APIExportEndpointSlice for %q has no endpoints yet", apiExportName)
		}

		return eps[0].URL, nil
	}

	return "", fmt.Errorf("no APIExportEndpointSlice found for APIExport %q", apiExportName)
}

// EndpointSliceName returns the name of the APIExportEndpointSlice whose
// spec.export.name matches apiExportName. The slice name is not guaranteed to
// equal the APIExport name (providers may create custom slices), so callers that
// need to construct a multicluster-provider (which is keyed by slice name) must
// resolve it dynamically. Mirrors dependency-controller/internal/kcp.FindEndpointSlice
// but returns just the name.
func EndpointSliceName(ctx context.Context, c client.Reader, apiExportName string) (string, error) {
	var list apisv1alpha1.APIExportEndpointSliceList
	if err := c.List(ctx, &list); err != nil {
		return "", fmt.Errorf("listing APIExportEndpointSlices: %w", err)
	}

	for i := range list.Items {
		if list.Items[i].Spec.APIExport.Name == apiExportName {
			return list.Items[i].Name, nil
		}
	}

	return "", fmt.Errorf("no APIExportEndpointSlice found for APIExport %q", apiExportName)
}

// ResolveKind resolves the Kind of a governed resource (identified by its plural
// resource name and API group) from the APIExport named apiExportName in the
// workspace reachable via c.
//
// A multicluster informer over the governed resource needs the fully-qualified
// GVK (unstructured watches cannot be set up from a GVR alone). The Kind is not
// present on the ConsumptionQuota spec, so it is resolved deterministically from
// the exported APIResourceSchema: the APIExport's spec.resources entry for the
// (group, resource) pair references an APIResourceSchema whose spec.names.kind
// is the answer. This avoids discovery round-trips against the virtual workspace.
func ResolveKind(ctx context.Context, c client.Reader, apiExportName, group, resource string) (string, error) {
	ex := &apisv1alpha2.APIExport{}
	if err := c.Get(ctx, client.ObjectKey{Name: apiExportName}, ex); err != nil {
		return "", fmt.Errorf("getting APIExport %q: %w", apiExportName, err)
	}

	var schemaName string
	for i := range ex.Spec.Resources {
		r := ex.Spec.Resources[i]
		if r.Group == group && r.Name == resource {
			schemaName = r.Schema
			break
		}
	}
	if schemaName == "" {
		return "", fmt.Errorf("APIExport %q does not export resource %q in group %q", apiExportName, resource, group)
	}

	schema := &apisv1alpha1.APIResourceSchema{}
	if err := c.Get(ctx, client.ObjectKey{Name: schemaName}, schema); err != nil {
		return "", fmt.Errorf("getting APIResourceSchema %q: %w", schemaName, err)
	}

	kind := schema.Spec.Names.Kind
	if kind == "" {
		return "", fmt.Errorf("APIResourceSchema %q has empty spec.names.kind", schemaName)
	}

	return kind, nil
}
