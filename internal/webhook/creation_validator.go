// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Package webhook serves the CREATE admission that enforces consumption quotas (spec §7.1).
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kcp-dev/logicalcluster/v3"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.opendefense.cloud/quota-controller/internal/identity"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// CreationValidator enforces one governed resource identity. One instance is mounted
// per (group,resource,identityHash) at /validate/{group}/{resource}/{identityHash}.
type CreationValidator struct {
	Reg   *registry.Registry
	Store *quota.Store

	ref registry.ResourceRef
	// ReserveFn is the reservation seam. In production it defaults to Store.Reserve;
	// in tests it is injected directly to control allow/deny without a live cluster.
	ReserveFn func(ctx context.Context, cluster, objKey string, limit int32) (bool, error)
}

// SetResource binds this validator to the identity parsed from the mount path.
// If ReserveFn is already set (e.g. by a test), it is not overwritten.
func (v *CreationValidator) SetResource(ref registry.ResourceRef) {
	v.ref = ref
	if v.ReserveFn == nil {
		v.ReserveFn = func(ctx context.Context, cluster, objKey string, limit int32) (bool, error) {
			return v.Store.Reserve(ctx, identity.UsageKey{
				Cluster:      cluster,
				Group:        ref.Group,
				Resource:     ref.Resource,
				IdentityHash: ref.IdentityHash,
			}, objKey, limit)
		}
	}
}

// Handle implements admission.Handler. It extracts the consumer logical cluster
// (via the kcp.io/cluster annotation, mirroring dep-ctrl's deletion_validator approach),
// looks up the quota limit from the registry, and reserves a slot via ReserveFn.
func (v *CreationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("not a create")
	}

	cluster := clusterFromRequest(req)
	if cluster == "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("missing kcp.io/cluster on object"))
	}

	limit, ok := v.Reg.LimitFor(cluster, v.ref)
	if !ok {
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("consumption quota policy for %s.%s not yet synced; denying (fail-closed)", v.ref.Resource, v.ref.Group))
	}

	objKey := req.Namespace + "/" + req.Name
	if req.Name == "" {
		// generateName: the apiserver assigns the name AFTER admission, so key on the
		// per-request UID. This never over-allows (each in-flight create gets a distinct
		// reservation); it over-holds until the TTL sweep, which errs strict (ADR-003).
		objKey = "uid:" + string(req.UID)
	}

	allowed, err := v.ReserveFn(ctx, cluster, objKey, limit)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err) // fail-closed on error
	}
	if !allowed {
		return admission.Denied(fmt.Sprintf(
			"consumption quota exceeded: at most %d %s per workspace", limit, v.ref.Resource,
		))
	}

	return admission.Allowed("within quota")
}

// clusterFromRequest extracts the kcp logical cluster name from the object's
// kcp.io/cluster annotation. This mirrors the extraction approach used by
// dep-ctrl's deletion_validator.go (logicalcluster.From reads the same annotation).
func clusterFromRequest(req admission.Request) string {
	src := req.Object.Raw
	if len(src) == 0 {
		src = req.OldObject.Raw
	}

	if len(src) == 0 {
		return ""
	}

	var pm metav1.PartialObjectMetadata
	if err := json.Unmarshal(src, &pm); err != nil {
		return ""
	}

	return pm.Annotations[logicalcluster.AnnotationKey]
}
