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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	v1alpha1 "go.opendefense.cloud/quota-controller/api/v1alpha1"
	"go.opendefense.cloud/quota-controller/internal/controller"
	"go.opendefense.cloud/quota-controller/internal/kcp"
	"go.opendefense.cloud/quota-controller/internal/quota"
	"go.opendefense.cloud/quota-controller/internal/registry"
	"go.opendefense.cloud/quota-controller/internal/selfservice"
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
		consumerAPIExportName   string
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
	flag.StringVar(&consumerAPIExportName, "consumer-api-export-name", "quota-consumer", "Name of the quota-consumer APIExport (QuotaClaims are served through its virtual workspace)")
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

	// Second multicluster manager over the quota-consumer APIExport VW: it
	// watches QuotaClaims across all consumer workspaces (spec §8). Same
	// construction as the quota-provider manager above, but without its own
	// metrics/health servers (port clashes) or leader election (gated by the
	// parent below).
	consumerSliceCtx, consumerSliceCancel := context.WithTimeout(rootCtx, startupRequestTimeout)
	consumerSliceName, err := kcp.EndpointSliceName(consumerSliceCtx, directClient, consumerAPIExportName)
	consumerSliceCancel()
	if err != nil {
		setupLog.Error(err, "unable to find APIExportEndpointSlice", "apiExport", consumerAPIExportName)
		os.Exit(1)
	}

	consumerProvider, err := apiexport.New(cfg, consumerSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create quota-consumer apiexport provider")
		os.Exit(1)
	}

	consumerMgr, err := mcmanager.New(cfg, consumerProvider, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false, // gated by the parent leader election below
	})
	if err != nil {
		setupLog.Error(err, "unable to create quota-consumer manager")
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

	// Provider-side policy index (ADR-002): the claim/grant reconcilers read
	// every authorization input (provider cluster, default, ceiling) from here,
	// never from consumer-writable objects. Fed by the CQ reconcile below.
	policies := selfservice.NewPolicyIndex()

	// Both self-service reconcilers write across workspaces through the two
	// quota VWs; resolve their URLs up front (bounded, like the slice lookups).
	providerVWCtx, providerVWCancel := context.WithTimeout(rootCtx, startupRequestTimeout)
	providerVWURL, err := kcp.VirtualWorkspaceURL(providerVWCtx, directClient, apiExportName)
	providerVWCancel()
	if err != nil {
		setupLog.Error(err, "unable to resolve virtual workspace URL", "apiExport", apiExportName)
		os.Exit(1)
	}

	consumerVWCtx, consumerVWCancel := context.WithTimeout(rootCtx, startupRequestTimeout)
	consumerVWURL, err := kcp.VirtualWorkspaceURL(consumerVWCtx, directClient, consumerAPIExportName)
	consumerVWCancel()
	if err != nil {
		setupLog.Error(err, "unable to resolve virtual workspace URL", "apiExport", consumerAPIExportName)
		os.Exit(1)
	}

	providerClientFor := vwScopedClientFactory(baseCfg, providerVWURL, scheme)
	consumerClientFor := vwScopedClientFactory(baseCfg, consumerVWURL, scheme)

	ensurer := &controller.ClaimEnsurer{ConsumerClientFor: consumerClientFor}

	// Must be assigned before mgr.Start: accounting sub-managers created during
	// CQ reconciles read these fields to wire their claim-discovery Runnable.
	acct.ensurer = ensurer
	acct.policies = policies

	// Multicluster ConsumptionQuota reconciler. The CQ reconcile plus the
	// accounting-informer and self-service-policy lifecycle hung off the same
	// event live in cqLifecycle.
	cqDriver := newCQLifecycle(mgr, baseCfg, scheme, reg, webhookBaseURL, caBundle, acct, policies)
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("consumptionquota").
		For(&v1alpha1.ConsumptionQuota{}).
		Complete(mcreconcile.Func(cqDriver.Reconcile)); err != nil {
		setupLog.Error(err, "unable to create ConsumptionQuota controller")
		os.Exit(1)
	}

	// Claim reconciler on the consumer manager: turns requestedLimit changes
	// into provider-workspace grants (spec §8 steps 2-3).
	claimReconciler := &controller.QuotaClaimReconciler{ProviderClientFor: providerClientFor, Policies: policies}
	if err := mcbuilder.ControllerManagedBy(consumerMgr).
		Named("quotaclaim").
		For(&v1alpha1.QuotaClaim{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			cl, err := consumerMgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			return claimReconciler.Reconcile(ctx, req.ClusterName.String(), cl.GetClient(), ctrl.Request{NamespacedName: req.NamespacedName})
		})); err != nil {
		setupLog.Error(err, "unable to create QuotaClaim controller")
		os.Exit(1)
	}

	// Grant reconciler on the provider manager: propagates decisions back to
	// the consumer-visible claim status (spec §8 step 5).
	grantReconciler := &controller.QuotaGrantReconciler{
		ConsumerClientFor: consumerClientFor,
		DefaultLimitFor: func(ref registry.ResourceRef) (int32, bool) {
			p, ok := policies.Get(ref)

			return p.DefaultLimit, ok
		},
		Now: time.Now,
	}
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("quotagrant").
		For(&v1alpha1.QuotaGrant{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			cl, err := mgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			return grantReconciler.Reconcile(ctx, cl.GetClient(), ctrl.Request{NamespacedName: req.NamespacedName})
		})); err != nil {
		setupLog.Error(err, "unable to create QuotaGrant controller")
		os.Exit(1)
	}

	// The consumer manager must only run on the leader (single writer for
	// grants/claims). Adding it directly would land it in controller-runtime's
	// Caches runnable group (mcManager promotes GetCache() from its embedded
	// manager.Manager, and the hasCache type-switch case is checked before the
	// leader-gated default), which starts on EVERY replica before leader
	// election is acquired. leaderGatedRunnable strips that cache-detection
	// surface so it takes the default, leader-gated path instead.
	if err := mgr.GetLocalManager().Add(leaderGatedRunnable(consumerMgr)); err != nil {
		setupLog.Error(err, "unable to attach quota-consumer manager")
		os.Exit(1)
	}

	setupLog.Info("starting manager")

	if err := mgr.Start(rootCtx); err != nil {
		setupLog.Error(err, "manager failed")
		os.Exit(1)
	}
}
