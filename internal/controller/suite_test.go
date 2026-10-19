// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"testing"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// k8sClient is the envtest client, available to all specs in this package.
// The suite hard-requires a working envtest toolchain (KUBEBUILDER_ASSETS);
// `task test` provisions it automatically via setup-envtest.
var k8sClient client.Client

// ctx is shared by specs that don't need per-test cancellation; existing
// specs mostly declare their own context.Background() locally, but the
// self-service integration specs (Task 7) reference a package-level ctx.
var ctx = context.Background()

var testEnv *envtest.Environment

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{"../../config/crds"},
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	sch := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(sch)).To(Succeed())
	Expect(v1alpha1.AddToScheme(sch)).To(Succeed())
	Expect(admissionregistrationv1.AddToScheme(sch)).To(Succeed())
	Expect(apisv1alpha2.AddToScheme(sch)).To(Succeed())

	k8sClient, err = client.New(cfg, client.Options{Scheme: sch})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	Expect(testEnv.Stop()).To(Succeed())
})
