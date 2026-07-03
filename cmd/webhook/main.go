// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Command webhook serves the CREATE admission webhook that enforces consumption
// quotas. It watches ConsumptionQuota across provider workspaces (via the
// quota-provider APIExport virtual workspace) to keep an in-memory limit
// registry, and reserves slots in the QuotaUsage ledgers on each admission.
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(apisv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apisv1alpha2.AddToScheme(scheme))
	utilruntime.Must(corev1alpha1.AddToScheme(scheme))
	utilruntime.Must(tenancyv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		apiExportName          string
		webhookPort            int
		tlsCertDir             string
		healthProbeBindAddress string
		reservationTTL         time.Duration
	)
	flag.StringVar(&apiExportName, "api-export-name", "quota-provider", "Name of the quota-provider APIExport (watched via its virtual workspace)")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port the admission webhook server binds to")
	flag.StringVar(&tlsCertDir, "tls-cert-dir", "/etc/webhook-tls", "Directory containing tls.crt and tls.key for the admission server")
	flag.StringVar(&healthProbeBindAddress, "health-probe-bind-address", ":8081", "Address to bind the health probe endpoint")
	flag.DurationVar(&reservationTTL, "reservation-ttl", 60*time.Second, "TTL for admission reservations (spec §7.4)")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	cfg := ctrl.GetConfigOrDie()

	if err := kcp.ValidateKubeconfig(cfg); err != nil {
		setupLog.Error(err, "invalid kubeconfig")
		os.Exit(1)
	}

	// Direct client to the quota-ctrl workspace: endpoint-slice discovery and the
	// backing client for the QuotaUsage Store.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create direct client")
		os.Exit(1)
	}

	sliceName, err := kcp.EndpointSliceName(context.Background(), directClient, apiExportName)
	if err != nil {
		setupLog.Error(err, "unable to find APIExportEndpointSlice", "apiExport", apiExportName)
		os.Exit(1)
	}

	provider, err := apiexport.New(cfg, sliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create quota-provider apiexport provider")
		os.Exit(1)
	}

	webhookOpts := ctrlwebhook.Options{
		Port:    webhookPort,
		CertDir: tlsCertDir,
	}

	// No leader election: every replica must serve admission (fail-closed).
	// The manager's WebhookServer owns TLS termination and cert hot-reload
	// (certwatcher), so the cert-manager-issued tls.crt/tls.key under tlsCertDir
	// are picked up without a restart on rotation.
	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthProbeBindAddress,
		WebhookServer:          ctrlwebhook.NewServer(webhookOpts),
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	reg := registry.New()
	store := &quota.Store{Client: directClient, TTL: reservationTTL, Now: time.Now}

	// Light watcher that mirrors ConsumptionQuota limits into the registry.
	watcher := &registryWatcher{
		mgr:           mgr,
		scheme:        scheme,
		apiExportName: apiExportName,
		reg:           reg,
		known:         map[string]registry.ResourceRef{},
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("registry-watcher").
		For(&v1alpha1.ConsumptionQuota{}).
		Complete(mcreconcile.Func(watcher.Reconcile)); err != nil {
		setupLog.Error(err, "unable to create registry watcher")
		os.Exit(1)
	}

	// Populate the registry before serving admission. Fail-closed: readyz stays
	// NOT ready until the initial ConsumptionQuota list has synced, so kcp does
	// not route CREATEs (which fail closed) at a webhook with an empty registry.
	initialized := make(chan struct{})
	if err := mgr.GetLocalManager().Add(manager.RunnableFunc(func(ctx context.Context) error {
		if err := watcher.PopulateRegistry(ctx); err != nil {
			return err
		}
		close(initialized)

		return nil
	})); err != nil {
		setupLog.Error(err, "unable to add registry population runnable")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("registry-populated", readyzCheck(initialized)); err != nil {
		setupLog.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	// A single dispatcher parses the mount path
	// (/validate/{group}/{resource}/{identityHash}) and routes to a per-identity
	// CreationValidator. Registering the subtree path lets the manager's
	// WebhookServer own the TLS listener; ServeMux forwards every /validate/...
	// request to the dispatcher with the full path intact.
	dispatcher := newValidatorDispatcher(reg, store, initialized, ctrl.Log.WithName("admission"))
	mgr.GetWebhookServer().Register("/validate/", dispatcher)

	setupLog.Info("starting webhook server")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "webhook server failed")
		os.Exit(1)
	}
}
