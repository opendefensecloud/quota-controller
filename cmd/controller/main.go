// Copyright 2026 BWI GmbH and Quota Controller contributors
// SPDX-License-Identifier: Apache-2.0

// Command controller runs the quota-controller reconcilers over the
// quota-provider APIExport virtual workspace: it resolves governed identities,
// stamps ConsumptionQuota status, installs the CREATE admission webhook, feeds
// the limit registry, and runs the accounting reconciler that keeps each
// QuotaUsage's confirmed count in sync with the live governed objects.
package main

import (
	"context"
	"flag"
	"os"
	"strings"
	"time"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
)

// startupRequestTimeout bounds one-shot network calls made during startup,
// before the manager (and its health endpoints) is running. Without it an
// unreachable kcp leaves the process hanging not-ready indefinitely.
const startupRequestTimeout = 30 * time.Second

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
		apiExportName           string
		webhookBaseURL          string
		webhookCABundlePath     string
		healthProbeBindAddress  string
		enableLeaderElection    bool
		leaderElectionID        string
		leaderElectionNamespace string
		reservationTTL          time.Duration
		resyncInterval          time.Duration
	)
	flag.StringVar(&apiExportName, "api-export-name", "quota-provider", "Name of the quota-provider APIExport (watched via its virtual workspace)")
	flag.StringVar(&webhookBaseURL, "webhook-base-url", "", "Base URL of the quota webhook server, e.g. https://quota-webhook.ns.svc:443. The installer appends /validate/<group>/<resource>/<identityHash>.")
	flag.StringVar(&webhookCABundlePath, "webhook-ca-bundle-path", "", "Path to the CA bundle PEM for the webhook server's TLS certificate")
	flag.StringVar(&healthProbeBindAddress, "health-probe-bind-address", ":8081", "Address to bind the health probe endpoint")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "Enable leader election so the accounting reconciler is a single writer")
	flag.StringVar(&leaderElectionID, "leader-election-id", "quota-controller.opendefense.cloud", "Name of the leader election lease")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "default", "Namespace holding the leader election lease")
	flag.DurationVar(&reservationTTL, "reservation-ttl", 60*time.Second, "TTL for admission reservations (spec §7.4)")
	flag.DurationVar(&resyncInterval, "resync-interval", 60*time.Second, "Interval for the accounting reservation sweep")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	// Refuse to run when the sweep interval exceeds the reservation TTL. The
	// sweep that refreshes confirmed counts must complete within the reservation
	// window; a longer resync means confirmed can stay stale past the TTL, which
	// risks over-allowing past the reservation guarantee (spec §7.4, R1 strict).
	if resyncInterval > reservationTTL {
		setupLog.Error(nil, "--resync-interval must not exceed --reservation-ttl: "+
			"a stale confirmed count past the reservation window can over-allow (spec §7.4)",
			"resyncInterval", resyncInterval, "reservationTTL", reservationTTL)
		os.Exit(1)
	}

	// Installed before any network I/O: rootCtx drives the whole process, the
	// accounting sub-managers derive their lifetime from it, and startup calls
	// below bound themselves on it so an unreachable kcp fails the pod fast
	// instead of hanging it not-ready forever (stalling rollouts).
	rootCtx := ctrl.SetupSignalHandler()

	cfg := ctrl.GetConfigOrDie()

	if err := kcp.ValidateKubeconfig(cfg); err != nil {
		setupLog.Error(err, "invalid kubeconfig")
		os.Exit(1)
	}

	// Front-proxy base config (no /clusters/<ws> suffix). Used to build
	// workspace-scoped configs for the per-governed-export accounting managers.
	baseCfg, err := kcp.BaseConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to derive front-proxy base URL from kubeconfig")
		os.Exit(1)
	}

	// Direct client to the quota-ctrl workspace (root:cloud-api-system): used
	// for endpoint-slice discovery of the quota-provider APIExport and as the
	// backing client for the shared QuotaUsage Store.
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create direct client")
		os.Exit(1)
	}

	sliceCtx, sliceCancel := context.WithTimeout(rootCtx, startupRequestTimeout)
	sliceName, err := kcp.EndpointSliceName(sliceCtx, directClient, apiExportName)
	sliceCancel()
	if err != nil {
		setupLog.Error(err, "unable to find APIExportEndpointSlice", "apiExport", apiExportName)
		os.Exit(1)
	}

	provider, err := apiexport.New(cfg, sliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create quota-provider apiexport provider")
		os.Exit(1)
	}

	mgr, err := mcmanager.New(cfg, provider, manager.Options{
		Scheme:                        scheme,
		HealthProbeBindAddress:        healthProbeBindAddress,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       leaderElectionNamespace,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	// Readyz is Ping: the controller has no safety-critical readiness gate.
	// If it reconciles before caches are warm it simply retries on the next event.
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	var caBundle []byte
	if webhookCABundlePath != "" {
		caBundle, err = os.ReadFile(webhookCABundlePath)
		if err != nil {
			setupLog.Error(err, "unable to read webhook CA bundle", "path", webhookCABundlePath)
			os.Exit(1)
		}
	}

	// Shared, in-memory (group,resource,identity)->limit index. In the controller
	// it exists so the reconciler can Delete stale entries symmetrically; the
	// webhook binary owns the enforcement copy.
	reg := registry.New()

	// One shared Store over the quota-ctrl workspace for all QuotaUsage ledgers.
	store := &quota.Store{Client: directClient, TTL: reservationTTL, Now: time.Now}

	acct := newAccountingManager(rootCtx, baseCfg, scheme, store, resyncInterval, ctrl.Log.WithName("accounting"))

	// Multicluster ConsumptionQuota reconciler. Because the VWC is installed in
	// the SAME workspace as the ConsumptionQuota (unlike dep-ctrl's cross-workspace
	// case), we build a per-reconcile reconciler + installer bound to the reconcile
	// workspace's own client. The accounting lifecycle is driven off the same event.
	reconcile := func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
		logger := ctrl.LoggerFrom(ctx).WithValues("cq", req.Name, "cluster", req.ClusterName)

		cl, err := mgr.GetCluster(ctx, req.ClusterName)
		if err != nil {
			return ctrl.Result{}, err
		}
		pcClient := cl.GetClient()

		// Direct (non-VW) client scoped to this reconcile's provider workspace.
		// The quota-provider VW does not serve apiexports, so the governed service
		// APIExport must be read through a direct connection to the provider
		// logical cluster. Mirrors the accounting manager's svcCfg construction.
		directCfg := rest.CopyConfig(baseCfg)
		directCfg.Host = strings.TrimRight(baseCfg.Host, "/") + "/clusters/" + req.ClusterName.String()
		directWSClient, err := client.New(directCfg, client.Options{Scheme: scheme})
		if err != nil {
			return ctrl.Result{}, err
		}

		cqr := &controller.ConsumptionQuotaReconciler{Client: pcClient, APIExportReader: directWSClient, Reg: reg}
		if webhookBaseURL != "" {
			cqr.Installer = controller.NewWebhookInstaller(pcClient, webhookBaseURL, caBundle)
		}

		res, err := cqr.Reconcile(ctx, ctrl.Request{NamespacedName: req.NamespacedName})
		if err != nil {
			return res, err
		}

		// Drive the accounting informer lifecycle from the same event. The re-Get
		// deliberately observes the object AFTER Reconcile's finalizer/status
		// writes: NotFound can only be seen once Reconcile has removed the
		// finalizer (webhook teardown done), so acct.Remove never races a webhook
		// that is still installed, and a stamped identityHash is guaranteed
		// current because Reconcile wrote it before returning.
		cq := &v1alpha1.ConsumptionQuota{}
		if gErr := pcClient.Get(ctx, req.NamespacedName, cq); gErr != nil {
			if apierrors.IsNotFound(gErr) {
				acct.Remove(req.ClusterName.String(), req.Name)
				return res, nil
			}

			return res, gErr
		}
		if !cq.DeletionTimestamp.IsZero() {
			acct.Remove(req.ClusterName.String(), req.Name)
			return res, nil
		}
		if cq.Status.IdentityHash == "" {
			// Identity not yet resolved; the reconciler above requeued. Accounting
			// waits until the identity is stamped.
			return res, nil
		}
		if err := acct.Ensure(ctx, req.ClusterName.String(), cq); err != nil {
			logger.Error(err, "unable to ensure accounting informer")
			return res, err
		}

		return res, nil
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("consumptionquota").
		For(&v1alpha1.ConsumptionQuota{}).
		Complete(mcreconcile.Func(reconcile)); err != nil {
		setupLog.Error(err, "unable to create ConsumptionQuota controller")
		os.Exit(1)
	}

	setupLog.Info("starting manager")

	if err := mgr.Start(rootCtx); err != nil {
		setupLog.Error(err, "manager failed")
		os.Exit(1)
	}
}
