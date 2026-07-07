// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package kcp_test

import (
	"context"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"go.opendefense.cloud/quota-controller/internal/kcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// endpointSlice builds an APIExportEndpointSlice advertising the given
// virtual-workspace URLs for apiExportName. On a multi-shard kcp there is one
// endpoint per shard.
func endpointSlice(name, apiExportName string, urls ...string) *apisv1alpha1.APIExportEndpointSlice {
	s := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{Name: apiExportName},
		},
	}
	for _, u := range urls {
		s.Status.APIExportEndpoints = append(s.Status.APIExportEndpoints,
			apisv1alpha1.APIExportEndpoint{URL: u})
	}

	return s
}

func fakeClientWith(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(apisv1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

var _ = Describe("VirtualWorkspaceURLs", func() {
	ctx := context.Background()

	It("returns every endpoint URL advertised for the matching APIExport", func() {
		c := fakeClientWith(endpointSlice("quota-provider", "quota-provider",
			"https://shard-1.example.com/services/apiexport/abc/quota-provider",
			"https://shard-2.example.com/services/apiexport/abc/quota-provider",
		))

		urls, err := kcp.VirtualWorkspaceURLs(ctx, c, "quota-provider")
		Expect(err).NotTo(HaveOccurred())
		Expect(urls).To(ConsistOf(
			"https://shard-1.example.com/services/apiexport/abc/quota-provider",
			"https://shard-2.example.com/services/apiexport/abc/quota-provider",
		))
	})

	It("dedupes repeated endpoint URLs", func() {
		c := fakeClientWith(endpointSlice("quota-provider", "quota-provider",
			"https://shard-1.example.com/services/apiexport/abc/quota-provider",
			"https://shard-1.example.com/services/apiexport/abc/quota-provider",
		))

		urls, err := kcp.VirtualWorkspaceURLs(ctx, c, "quota-provider")
		Expect(err).NotTo(HaveOccurred())
		Expect(urls).To(HaveLen(1))
	})

	It("errors while the slice has no endpoints yet", func() {
		c := fakeClientWith(endpointSlice("quota-provider", "quota-provider"))

		_, err := kcp.VirtualWorkspaceURLs(ctx, c, "quota-provider")
		Expect(err).To(MatchError(ContainSubstring("no endpoints yet")))
	})

	It("errors when no slice matches the APIExport", func() {
		c := fakeClientWith(endpointSlice("other", "other-export", "https://x.example.com"))

		_, err := kcp.VirtualWorkspaceURLs(ctx, c, "quota-provider")
		Expect(err).To(MatchError(ContainSubstring("no APIExportEndpointSlice found")))
	})
})

var _ = Describe("VirtualWorkspaceURL", func() {
	It("returns the first endpoint URL", func() {
		c := fakeClientWith(endpointSlice("quota-provider", "quota-provider",
			"https://shard-1.example.com/vw", "https://shard-2.example.com/vw",
		))

		url, err := kcp.VirtualWorkspaceURL(context.Background(), c, "quota-provider")
		Expect(err).NotTo(HaveOccurred())
		Expect(url).To(Equal("https://shard-1.example.com/vw"))
	})
})
