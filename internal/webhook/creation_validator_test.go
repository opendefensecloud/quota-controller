// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
	whook "go.opendefense.cloud/quota-controller/internal/webhook"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// createReq builds a synthetic CREATE admission.Request whose object carries the
// kcp.io/cluster annotation so clusterFromRequest can extract it.
//
//nolint:unparam // cluster is parameterised for clarity; test callers currently all use "root:c1".
func createReq(cluster, group, resource, ns, name string) admission.Request {
	return createReqFull(cluster, group, resource, ns, name, "")
}

// createReqWithUID builds a CREATE request with an empty name (generateName path)
// and an explicit request UID.
func createReqWithUID(cluster, group, resource, ns string, uid types.UID) admission.Request {
	return createReqFull(cluster, group, resource, ns, "", uid)
}

// createReqFull is the underlying builder used by createReq and createReqWithUID.
func createReqFull(cluster, group, resource, ns, name string, uid types.UID) admission.Request {
	meta := map[string]any{
		"namespace":   ns,
		"annotations": map[string]any{"kcp.io/cluster": cluster},
	}
	if name != "" {
		meta["name"] = name
	}

	obj := map[string]any{
		"apiVersion": group + "/v1",
		"kind":       "Bucket",
		"metadata":   meta,
	}

	raw, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: admissionv1.Create,
		Resource:  metav1.GroupVersionResource{Group: group, Version: "v1", Resource: resource},
		Namespace: ns,
		Name:      name,
		UID:       uid,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

var _ = Describe("CreationValidator", func() {
	var (
		reg *registry.Registry
		ref registry.ResourceRef
	)

	BeforeEach(func() {
		reg = registry.New()
		ref = registry.ResourceRef{Group: "s3.example.com", Resource: "buckets", IdentityHash: "h1"}
		reg.Set(ref, 3)
	})

	It("denies when the store denies (quota exhausted)", func() {
		v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
		v.SetResource(ref)
		v.ReserveFn = func(_ context.Context, _, _ string, _ int32) (bool, error) { return false, nil }

		resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))

		Expect(resp.Allowed).To(BeFalse())
	})

	It("allows when the store allows (under limit)", func() {
		v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
		v.SetResource(ref)
		v.ReserveFn = func(_ context.Context, _, _ string, _ int32) (bool, error) { return true, nil }

		resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))

		Expect(resp.Allowed).To(BeTrue())
	})

	// Fail-closed, security-critical branch: an error from ReserveFn must produce DENY + HTTP 500.
	It("denies with HTTP 500 when ReserveFn returns an error (fail-closed)", func() {
		v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
		v.SetResource(ref)
		v.ReserveFn = func(_ context.Context, _, _ string, _ int32) (bool, error) {
			return false, errors.New("boom")
		}

		resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))

		Expect(resp.Allowed).To(BeFalse())
		Expect(resp.Result).NotTo(BeNil())
		Expect(resp.Result.Code).To(BeNumerically("==", http.StatusInternalServerError))
	})

	// Fail-closed miss: when no quota policy is configured the handler must deny (HTTP 500).
	It("denies with HTTP 500 when no quota policy is configured (fail-closed miss)", func() {
		emptyReg := registry.New() // ref is absent
		v := &whook.CreationValidator{Reg: emptyReg, Store: &quota.Store{TTL: time.Minute}}
		v.SetResource(ref)

		resp := v.Handle(context.Background(), createReq("root:c1", "s3.example.com", "buckets", "ns", "b1"))

		Expect(resp.Allowed).To(BeFalse())
		Expect(resp.Result).NotTo(BeNil())
		Expect(resp.Result.Code).To(BeNumerically("==", http.StatusInternalServerError))
	})

	// generateName: empty-name requests must use uid:<UID> as the reservation key.
	Describe("generateName objKey construction", func() {
		It("keys a generateName request on uid:<UID>", func() {
			var capturedKey string
			v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
			v.SetResource(ref)
			v.ReserveFn = func(_ context.Context, _, key string, _ int32) (bool, error) {
				capturedKey = key
				return true, nil
			}

			uid := types.UID("test-uid-1234")
			resp := v.Handle(context.Background(),
				createReqWithUID("root:c1", "s3.example.com", "buckets", "ns", uid))

			Expect(resp.Allowed).To(BeTrue())
			Expect(capturedKey).To(Equal("uid:" + string(uid)))
		})

		It("assigns distinct keys to generateName requests with different UIDs", func() {
			keys := make([]string, 0, 2)
			v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
			v.SetResource(ref)
			v.ReserveFn = func(_ context.Context, _, key string, _ int32) (bool, error) {
				keys = append(keys, key)
				return true, nil
			}

			v.Handle(context.Background(), //nolint:errcheck
				createReqWithUID("root:c1", "s3.example.com", "buckets", "ns", types.UID("uid-A")))
			v.Handle(context.Background(), //nolint:errcheck
				createReqWithUID("root:c1", "s3.example.com", "buckets", "ns", types.UID("uid-B")))

			Expect(keys).To(HaveLen(2))
			Expect(keys[0]).To(Equal("uid:uid-A"))
			Expect(keys[1]).To(Equal("uid:uid-B"))
			Expect(keys[0]).NotTo(Equal(keys[1]))
		})

		It("keys a named request on <namespace>/<name>", func() {
			var capturedKey string
			v := &whook.CreationValidator{Reg: reg, Store: &quota.Store{TTL: time.Minute}}
			v.SetResource(ref)
			v.ReserveFn = func(_ context.Context, _, key string, _ int32) (bool, error) {
				capturedKey = key
				return true, nil
			}

			resp := v.Handle(context.Background(),
				createReq("root:c1", "s3.example.com", "buckets", "ns", "my-bucket"))

			Expect(resp.Allowed).To(BeTrue())
			Expect(capturedKey).To(Equal("ns/my-bucket"))
		})
	})
})
