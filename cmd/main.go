// The cloudflare-operator manager.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflared"
	"github.com/miho-labs/cloudflare-operator/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	version  = "dev"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		secureMetrics        bool
		resyncPeriod         time.Duration
		cloudflaredImage     string
		watchNamespaces      string
		apiBaseURL           string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to. Use 0 to disable.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the health probes bind to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "Enable leader election so only one replica reconciles.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false, "Serve metrics over HTTPS with self-signed certificates.")
	flag.DurationVar(&resyncPeriod, "resync-period", controller.DefaultResyncPeriod, "How often every resource is re-checked against Cloudflare for drift.")
	flag.StringVar(&cloudflaredImage, "cloudflared-image", cloudflared.DefaultImage, "Default cloudflared image for Tunnel connectors.")
	flag.StringVar(&watchNamespaces, "watch-namespaces", "", "Comma-separated namespaces to watch. Empty watches every namespace.")
	flag.StringVar(&apiBaseURL, "cloudflare-api-base-url", "", "Override the Cloudflare API base URL for every account (testing).")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog.Info("starting cloudflare-operator", "version", version)

	cacheOpts := cache.Options{}
	if watchNamespaces != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{}
		for _, ns := range strings.Split(watchNamespaces, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				cacheOpts.DefaultNamespaces[ns] = cache.Config{}
			}
		}
	}
	metricsOpts := metricsserver.Options{BindAddress: metricsAddr, SecureServing: secureMetrics}
	if secureMetrics {
		metricsOpts.TLSOpts = []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS12 }}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsOpts,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "cloudflare-operator.miho-labs.dev",
		Cache:                  cacheOpts,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := registerIndexes(mgr); err != nil {
		setupLog.Error(err, "unable to register field indexes")
		os.Exit(1)
	}

	clients := controller.NewClientFactory(mgr.GetClient())
	clients.BaseURL = apiBaseURL
	clients.UserAgent = "cloudflare-operator/" + version
	rec := mgr.GetEventRecorderFor("cloudflare-operator")

	reconcilers := []interface{ SetupWithManager(ctrl.Manager) error }{
		&controller.CloudflareAccountReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients},
		&controller.DNSRecordReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
		&controller.TunnelReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod, CloudflaredImage: cloudflaredImage},
		&controller.TunnelIngressReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
		&controller.R2BucketReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
		&controller.AccessGroupReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
		&controller.AccessServiceTokenReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
		&controller.AccessApplicationReconciler{Client: mgr.GetClient(), Recorder: rec, Clients: clients, ResyncPeriod: resyncPeriod},
	}
	for _, r := range reconcilers {
		if err := r.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// registerIndexes adds the field indexes the controllers query. Keep in sync with
// internal/controller/suite_test.go.
func registerIndexes(mgr ctrl.Manager) error {
	ctx := context.Background()
	indexer := mgr.GetFieldIndexer()
	for _, obj := range []client.Object{
		&v1alpha1.DNSRecord{}, &v1alpha1.Tunnel{}, &v1alpha1.R2Bucket{},
		&v1alpha1.AccessApplication{}, &v1alpha1.AccessGroup{}, &v1alpha1.AccessServiceToken{},
	} {
		if err := indexer.IndexField(ctx, obj, controller.IndexAccountRef, controller.AccountRefIndexer); err != nil {
			return err
		}
	}
	if err := indexer.IndexField(ctx, &v1alpha1.TunnelIngress{}, controller.IndexTunnelRef, controller.TunnelRefIndexer); err != nil {
		return err
	}
	return indexer.IndexField(ctx, &v1alpha1.CloudflareAccount{}, controller.IndexSecretRef, controller.SecretRefIndexer)
}
