package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

func main() {
	var (
		listen        = flag.String("listen", ":9091", "HTTP listen address")
		pollInterval  = flag.Duration("metrics-poll-interval", 5*time.Second, "Metrics collection interval")
		metricsRet    = flag.Duration("metrics-retention", 72*time.Hour, "Metrics retention duration")
		logRet        = flag.Duration("log-retention", 48*time.Hour, "Log retention duration")
		trafficRet    = flag.Duration("traffic-retention", 48*time.Hour, "Traffic retention duration")
		maxLogPods    = flag.Int("max-log-pods", 100, "Maximum number of pods to tail logs from")
		storagePath   = flag.String("storage-path", "/data", "Path for SQLite database")
		enableTraffic = flag.Bool("enable-traffic", true, "Enable traffic collection from ingress access logs")
		ingressNs     = flag.String("ingress-namespace", "mortise-deps", "Namespace where ingress controller pods run")
		trafficBucket = flag.Duration("traffic-bucket-size", 5*time.Second, "Traffic aggregation bucket size")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := kubeConfig()
	if err != nil {
		log.Error("failed to get kubernetes config", "error", err)
		os.Exit(1)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error("failed to create kubernetes client", "error", err)
		os.Exit(1)
	}

	mc, err := metricsv.NewForConfig(cfg)
	if err != nil {
		log.Warn("failed to create metrics client; metrics collection disabled", "error", err)
	}

	store, err := NewStore(*storagePath + "/observer.db")
	if err != nil {
		log.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	liveCache := NewLiveMetricsCache(2 * time.Hour)
	liveTrafficCache := NewLiveTrafficCache(2 * time.Hour)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if mc != nil {
		metricsCollector := NewMetricsCollector(cs, mc, store, liveCache, *pollInterval, log)
		go metricsCollector.Run(ctx)
	}

	logCollector := NewLogCollector(cs, store, *pollInterval, *maxLogPods, log)
	go logCollector.Run(ctx)

	if *enableTraffic {
		trafficCollector := NewTrafficCollector(cs, store, liveTrafficCache, *pollInterval, *trafficBucket, *ingressNs, log)
		go trafficCollector.Run(ctx)
	}

	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				md, ld, err := store.Trim(*metricsRet, *logRet, *trafficRet)
				if err != nil {
					log.Error("trim failed", "error", err)
				} else if md > 0 || ld > 0 {
					log.Info("trimmed old data", "metricsDeleted", md, "logsDeleted", ld)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:    *listen,
		Handler: NewObserverServer(store, liveCache, liveTrafficCache),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Info("starting observer", "addr", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func kubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
