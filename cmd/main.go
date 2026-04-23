/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/admission"
	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/build"
	"github.com/mortise-org/mortise/internal/controller"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/ingress"
	"github.com/mortise-org/mortise/internal/platformconfig"
	"github.com/mortise-org/mortise/internal/registry"
	"github.com/mortise-org/mortise/internal/ui"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(mortisev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stacks bundles the three pluggable clients the AppReconciler needs plus any
// non-client platform settings the controller has to thread through.
type stacks struct {
	build    build.BuildClient
	registry *registry.OCIBackend
	git      git.GitClient

	// TLS is the resolved cert-manager / TLS configuration from PlatformConfig.
	// Currently only CertManagerClusterIssuer is consumed (by the App
	// reconciler); kept as a struct so future TLS settings can land here
	// without another plumbing churn.
	TLS platformconfig.TLSConfig
}

// buildStacks constructs the registry / build / git clients from the
// singleton PlatformConfig if present, falling back to env-var defaults so the
// operator can still start (and the API/UI stay reachable) when a user hasn't
// created the PlatformConfig yet.
//
// Fallback env vars (emergency use only — normal path is PlatformConfig):
//
//	MORTISE_BUILDKIT_ADDR     buildkitd address (default: tcp://buildkitd.mortise-system.svc:1234)
//	MORTISE_REGISTRY_URL      OCI registry URL (default: http://zot.mortise-system.svc:5000)
//	MORTISE_REGISTRY_USERNAME registry username (default: empty)
//	MORTISE_REGISTRY_PASSWORD registry password (default: empty)
//
// Note: changes to the PlatformConfig CRD require an operator restart to take
// effect (no hot reload in v1).
func buildStacks(ctx context.Context, reader client.Reader, log logr.Logger) stacks {
	cfg, err := platformconfig.Load(ctx, reader)
	if err == nil {
		return stacksFromPlatformConfig(cfg, log)
	}
	if errors.Is(err, platformconfig.ErrNotFound) {
		log.Info("PlatformConfig \"platform\" not found; using env-var fallback. " +
			"Git-source apps will build as soon as a PlatformConfig is created and the operator is restarted.")
	} else {
		log.Error(err, "Failed to load PlatformConfig; using env-var fallback")
	}
	return stacksFromEnv(log)
}

// stacksFromPlatformConfig builds the stacks from a resolved PlatformConfig.
// BuildKit TLS material (PEM) is written to temp files because bkclient
// expects file paths, not in-memory PEM.
func stacksFromPlatformConfig(cfg *platformconfig.Config, log logr.Logger) stacks {
	buildCfg := build.Config{
		Addr:            cfg.Build.BuildkitAddr,
		DefaultPlatform: cfg.Build.DefaultPlatform,
	}
	if cfg.Build.TLSCA != "" || cfg.Build.TLSCert != "" {
		if err := writeBuildKitTLS(&buildCfg, cfg.Build); err != nil {
			log.Error(err, "Failed to materialise BuildKit TLS material; continuing without TLS")
		}
	}

	var bc build.BuildClient
	if buildCfg.Addr == "" {
		log.Info("PlatformConfig has no spec.build.buildkitAddr; git-source apps will not build")
	} else if bk, err := build.New(buildCfg); err != nil {
		log.Error(err, "Failed to connect to buildkitd; git-source apps will not build", "addr", buildCfg.Addr)
	} else {
		bc = bk
	}

	return stacks{
		build: bc,
		registry: registry.NewOCIBackend(registry.Config{
			URL:                   cfg.Registry.URL,
			Namespace:             cfg.Registry.Namespace,
			Username:              cfg.Registry.Username,
			Password:              cfg.Registry.Password,
			PullSecretName:        cfg.Registry.PullSecretName,
			InsecureSkipTLSVerify: cfg.Registry.InsecureSkipTLSVerify,
			PullURL:               cfg.Registry.PullURL,
		}),
		git: git.NewGoGitClient(),
		TLS: cfg.TLS,
	}
}

// stacksFromEnv is the fallback path when PlatformConfig doesn't exist yet.
// Defaults match `make dev-up` so a fresh cluster boots without hand-written
// env. Once a PlatformConfig is created, an operator restart switches to it.
func stacksFromEnv(log logr.Logger) stacks {
	buildkitAddr := envOrDefault("MORTISE_BUILDKIT_ADDR", "tcp://buildkitd.mortise-system.svc:1234")
	registryURL := envOrDefault("MORTISE_REGISTRY_URL", "http://zot.mortise-system.svc:5000")

	var bc build.BuildClient
	if bk, err := build.New(build.Config{Addr: buildkitAddr}); err != nil {
		log.Error(err, "Failed to connect to buildkitd; git-source apps will not build", "addr", buildkitAddr)
	} else {
		bc = bk
	}

	return stacks{
		build: bc,
		registry: registry.NewOCIBackend(registry.Config{
			URL:                   registryURL,
			Username:              os.Getenv("MORTISE_REGISTRY_USERNAME"),
			Password:              os.Getenv("MORTISE_REGISTRY_PASSWORD"),
			InsecureSkipTLSVerify: false,
		}),
		git: git.NewGoGitClient(),
	}
}

// writeBuildKitTLS materialises PEM strings from PlatformConfig into temp files
// and sets the corresponding path fields on buildCfg. bkclient requires file
// paths; holding the PEM in memory isn't enough.
func writeBuildKitTLS(buildCfg *build.Config, src platformconfig.BuildConfig) error {
	dir, err := os.MkdirTemp("", "mortise-buildkit-tls-*")
	if err != nil {
		return err
	}
	write := func(name, pem string) (string, error) {
		if pem == "" {
			return "", nil
		}
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(pem), 0o600)
	}
	if p, err := write("ca.crt", src.TLSCA); err != nil {
		return err
	} else {
		buildCfg.TLSCACert = p
	}
	if p, err := write("tls.crt", src.TLSCert); err != nil {
		return err
	} else {
		buildCfg.TLSCert = p
	}
	if p, err := write("tls.key", src.TLSKey); err != nil {
		return err
	} else {
		buildCfg.TLSKey = p
	}
	return nil
}

func isCRDDiscoveryNotReady(err error) bool {
	if err == nil {
		return false
	}

	var noKindMatchErr *meta.NoKindMatchError
	if errors.As(err, &noKindMatchErr) {
		return true
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "no matches for kind") ||
		strings.Contains(errText, "failed to get restmapping")
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var apiAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.StringVar(&apiAddr, "api-bind-address", ":8090", "The address the REST API server binds to.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "00121253.mortise.dev",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.ProjectReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "Project")
		os.Exit(1)
	}

	// Build the registry / build / git clients from PlatformConfig (singleton
	// named "platform"). If PlatformConfig doesn't exist yet, fall back to
	// MORTISE_* env-var defaults so the operator still starts and the API/UI
	// remain reachable for initial setup. See buildStacks for details.
	//
	// We construct a direct client (bypassing the manager's cache, which isn't
	// started yet at this point) just for the one-shot Load call.
	directReader, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Failed to create direct API reader for PlatformConfig load")
		os.Exit(1)
	}
	stk := buildStacks(context.Background(), directReader, setupLog)

	ingressProvider := ingress.NewAnnotationProvider(ingress.AnnotationProviderConfig{
		ClassName:            os.Getenv("MORTISE_INGRESS_CLASS"),
		DefaultClusterIssuer: stk.TLS.CertManagerClusterIssuer,
		Reader:               mgr.GetClient(),
	})

	appReconciler := &controller.AppReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		BuildClient:     stk.build,
		GitClient:       stk.git,
		RegistryBackend: stk.registry,
		IngressProvider: ingressProvider,
	}
	if err := appReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "App")
		os.Exit(1)
	}
	if err := (&controller.PlatformConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PlatformConfig")
		os.Exit(1)
	}
	if err := (&controller.GitProviderReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "GitProvider")
		os.Exit(1)
	}
	if err := (&controller.PreviewEnvironmentReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		BuildClient:     stk.build,
		GitClient:       stk.git,
		RegistryBackend: stk.registry,
		IngressProvider: ingressProvider,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PreviewEnvironment")
		os.Exit(1)
	}
	// Admission webhooks — enforce cross-resource invariants (App env names
	// must exist on the parent Project; a Project can't delete its last env
	// or remove an env still referenced by an App override).
	//
	// Wiring is conditional on webhook TLS being configured, so `make dev-up`
	// continues to work without cert-manager churn. Production installs pass
	// --webhook-cert-path and the webhooks engage automatically.
	if webhookCertPath != "" {
		if err := (&admission.AppValidator{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
			if isCRDDiscoveryNotReady(err) {
				setupLog.Info("Skipped admission webhook registration because CRDs are not yet discoverable",
					"webhook", "App", "error", err)
			} else {
				setupLog.Error(err, "Failed to register admission webhook", "webhook", "App")
				os.Exit(1)
			}
		}
		if err := (&admission.ProjectValidator{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
			if isCRDDiscoveryNotReady(err) {
				setupLog.Info("Skipped admission webhook registration because CRDs are not yet discoverable",
					"webhook", "Project", "error", err)
			} else {
				setupLog.Error(err, "Failed to register admission webhook", "webhook", "Project")
				os.Exit(1)
			}
		}
	} else {
		setupLog.Info("Webhook TLS not configured — admission validators disabled; same checks still run in the REST API layer",
			"hint", "pass --webhook-cert-path to enable admission webhooks")
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	// Start REST API server alongside the controller manager.
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Failed to create kubernetes clientset")
		os.Exit(1)
	}

	dynamicClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Failed to create dynamic client")
		os.Exit(1)
	}

	authProvider := auth.NewNativeAuthProvider(mgr.GetClient())
	jwtHelper := auth.NewJWTHelper(mgr.GetClient())

	var uiSub fs.FS
	if sub, err := ui.FS(); err == nil {
		uiSub = sub
	} else {
		setupLog.Info("UI files not available; API will still serve", "err", err)
	}

	apiServer := api.NewServer(mgr.GetClient(), clientset, dynamicClient, mgr.GetConfig(), authProvider, jwtHelper, uiSub, authz.NewNativePolicyEngine(mgr.GetClient()))
	apiServer.SetBuildLogProvider(&appReconciler.Builds)
	if mc, err := metricsv.NewForConfig(mgr.GetConfig()); err == nil {
		apiServer.SetMetricsClient(mc.MetricsV1beta1())
	} else {
		setupLog.Info("metrics-server client unavailable, real-time metrics disabled", "error", err)
	}
	httpServer := &http.Server{Addr: apiAddr, Handler: apiServer.Handler()}
	go func() {
		setupLog.Info("Starting API server", "addr", apiAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			setupLog.Error(err, "Failed to start API server")
			os.Exit(1)
		}
	}()

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
