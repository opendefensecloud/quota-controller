// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/webhook"
)

// validatorDispatcher is a single HTTP handler that routes admission requests
// mounted at /validate/{group}/{resource}/{identityHash} to a per-identity
// CreationValidator, lazily constructing and caching one per resource ref.
type validatorDispatcher struct {
	reg         *registry.Registry
	store       *quota.Store
	initialized <-chan struct{}
	log         logr.Logger

	mu       sync.Mutex
	handlers map[registry.ResourceRef]http.Handler
}

func newValidatorDispatcher(reg *registry.Registry, store *quota.Store, initialized <-chan struct{}, log logr.Logger) *validatorDispatcher {
	return &validatorDispatcher{
		reg:         reg,
		store:       store,
		initialized: initialized,
		log:         log,
		handlers:    map[registry.ResourceRef]http.Handler{},
	}
}

func (d *validatorDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Fail-closed until the registry is populated: refuse to serve so callers do
	// not receive spurious allows before limits are known.
	select {
	case <-d.initialized:
	default:
		http.Error(w, "quota registry not yet populated", http.StatusServiceUnavailable)

		return
	}

	ref, ok := parseValidatePath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid validate path, expected /validate/{group}/{resource}/{identityHash}", http.StatusNotFound)

		return
	}

	d.handlerFor(ref).ServeHTTP(w, r)
}

// handlerFor returns the admission handler for ref, constructing it on first use.
func (d *validatorDispatcher) handlerFor(ref registry.ResourceRef) http.Handler {
	d.mu.Lock()
	defer d.mu.Unlock()

	if h, ok := d.handlers[ref]; ok {
		return h
	}

	v := &webhook.CreationValidator{Reg: d.reg, Store: d.store}
	v.SetResource(ref)
	h := &admission.Webhook{Handler: v}
	d.handlers[ref] = h

	return h
}

// parseValidatePath parses /validate/{group}/{resource}/{identityHash}. The group
// segment may be empty (core group); resource and identityHash must be present.
func parseValidatePath(p string) (registry.ResourceRef, bool) {
	const prefix = "/validate/"
	if !strings.HasPrefix(p, prefix) {
		return registry.ResourceRef{}, false
	}

	parts := strings.Split(strings.TrimPrefix(p, prefix), "/")
	if len(parts) != 3 {
		return registry.ResourceRef{}, false
	}
	if parts[1] == "" || parts[2] == "" {
		return registry.ResourceRef{}, false
	}

	return registry.ResourceRef{Group: parts[0], Resource: parts[1], IdentityHash: parts[2]}, true
}
