// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// leaderGatedRunnable wraps a multicluster manager so controller-runtime
// places it in the leader-election runnable group. Adding the manager
// directly would land it in the Caches group — mcManager promotes GetCache()
// from its embedded manager, and runnables.Add checks hasCache before the
// leader-gated default — which starts BEFORE leader election on every
// replica and would break the single-writer invariant for grant/claim
// writes. RunnableFunc's method set is only Start, so it takes the default,
// leader-gated path.
//
// m.Start is called through a closure rather than taken as a method value:
// a method value on an interface evaluates (and panics on) a nil receiver
// immediately, at wrap time, whereas the closure only dereferences m when
// the runnable is actually started.
func leaderGatedRunnable(m mcmanager.Manager) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		return m.Start(ctx)
	})
}

// diffConsumers returns which consumers to add and which to remove, given the
// currently-bound set and the set claims were last ensured for.
func diffConsumers(bound, ensured sets.Set[string]) (add, remove []string) {
	return sets.List(bound.Difference(ensured)), sets.List(ensured.Difference(bound))
}

// vwScopedClientFactory returns per-logical-cluster clients through a virtual
// workspace: {vwURL}/clusters/{cluster}.
func vwScopedClientFactory(baseCfg *rest.Config, vwURL string, scheme *runtime.Scheme) func(context.Context, string) (client.Client, error) {
	return func(_ context.Context, cluster string) (client.Client, error) {
		return kcp.VWClient(baseCfg, vwURL, cluster, scheme)
	}
}

// claimEnsurer abstracts the per-consumer claim create/refresh/GC operations
// claimDiscovery drives each sweep. Production wires in *controller.ClaimEnsurer
// (satisfied structurally); tests use a stub so sweep resilience (per-consumer
// error handling, ensure-all-every-tick) can be verified without a real client.
type claimEnsurer interface {
	Ensure(ctx context.Context, consumerCluster string, ref registry.ResourceRef, defaultLimit int32) error
	Remove(ctx context.Context, consumerCluster string, ref registry.ResourceRef) error
}

// claimDiscovery periodically lists APIBindings through one governed export's
// service VW (ADR-005) and reconciles the pre-created QuotaClaim set: every
// bound consumer -> Ensure (idempotent; also refreshes status.effectiveLimit
// for phase-None claims when the policy default changes — see I2); ensured
// consumer no longer bound -> Remove (GC). Runs inside the per-export
// accounting sub-manager, so its lifetime and leader-election gating match
// the export's accounting.
type claimDiscovery struct {
	svcVWClient  client.Client // list APIBindings across {serviceVW}/clusters/*
	exportName   string        // governed service export the bindings must reference
	ref          registry.ResourceRef
	defaultLimit func() (int32, bool) // policy default at sweep time (PolicyIndex-backed)
	ensurer      claimEnsurer
	interval     time.Duration
	log          logr.Logger

	ensured sets.Set[string]
}

func (d *claimDiscovery) Start(ctx context.Context) error {
	d.ensured = sets.New[string]()
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		if err := d.sweep(ctx); err != nil {
			d.log.Error(err, "claim discovery sweep failed", "export", d.exportName)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (d *claimDiscovery) sweep(ctx context.Context) error {
	var bindings apisv1alpha1.APIBindingList
	if err := d.svcVWClient.List(ctx, &bindings); err != nil {
		return fmt.Errorf("listing APIBindings via service VW: %w", err)
	}

	bound := sets.New[string]()
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Spec.Reference.Export == nil || b.Spec.Reference.Export.Name != d.exportName {
			continue
		}
		bound.Insert(logicalcluster.From(b).String())
	}

	limit, ok := d.defaultLimit()
	if !ok {
		d.log.Info("no policy default yet; skipping claim sweep", "export", d.exportName)

		return nil
	}

	// I2: Ensure runs for every currently-bound consumer on every sweep (not
	// just newly-bound ones) so a phase-None claim's status.effectiveLimit
	// stays current when the CQ's defaultLimit changes. Only the remove side
	// stays diff-based — GC only needs to act once per unbind.
	_, remove := diffConsumers(bound, d.ensured)

	return d.apply(ctx, sets.List(bound), remove, limit)
}

// apply drives Ensure over add and Remove over remove. I1: a per-consumer
// error is logged and skipped rather than aborting the sweep — one
// misbehaving or unreachable consumer workspace must not starve the rest
// (adversarial-consumer threat model). Errors are aggregated and returned so
// Start still logs a sweep-level failure, but d.ensured reflects exactly
// which consumers succeeded: a failed Ensure is not recorded (so it's
// retried next tick) and a failed Remove keeps its consumer in d.ensured (so
// GC is retried next tick).
func (d *claimDiscovery) apply(ctx context.Context, add, remove []string, limit int32) error {
	var errs []error
	for _, consumer := range add {
		if err := d.ensurer.Ensure(ctx, consumer, d.ref, limit); err != nil {
			d.log.Error(err, "ensuring claim failed for consumer; will retry next sweep", "export", d.exportName, "consumer", consumer)
			errs = append(errs, fmt.Errorf("ensure claim for %s: %w", consumer, err))

			continue
		}
		d.ensured.Insert(consumer)
	}
	for _, consumer := range remove {
		if err := d.ensurer.Remove(ctx, consumer, d.ref); err != nil {
			d.log.Error(err, "removing claim failed for consumer; will retry next sweep", "export", d.exportName, "consumer", consumer)
			errs = append(errs, fmt.Errorf("remove claim for %s: %w", consumer, err))

			continue
		}
		d.ensured.Delete(consumer)
	}

	return errors.Join(errs...)
}
