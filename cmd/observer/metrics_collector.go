package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

type MetricsCollector struct {
	clientset     kubernetes.Interface
	metricsClient metricsv.Interface
	store         *Store
	liveCache     *LiveMetricsCache
	interval      time.Duration
	log           *slog.Logger
}

func NewMetricsCollector(cs kubernetes.Interface, mc metricsv.Interface, store *Store, liveCache *LiveMetricsCache, interval time.Duration, log *slog.Logger) *MetricsCollector {
	return &MetricsCollector{
		clientset:     cs,
		metricsClient: mc,
		store:         store,
		liveCache:     liveCache,
		interval:      interval,
		log:           log,
	}
}

func (c *MetricsCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *MetricsCollector) collect(ctx context.Context) {
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.log.Error("failed to list namespaces", "error", err)
		return
	}

	now := time.Now().Unix()
	var entries []MetricEntry

	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, "pj-") || !isEnvNamespace(ns.Name) {
			continue
		}

		podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(ns.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			c.log.Warn("failed to list pod metrics", "namespace", ns.Name, "error", err)
			continue
		}

		for _, pm := range podMetrics.Items {
			appName := pm.Labels["app.kubernetes.io/name"]
			envName := pm.Labels["mortise.dev/environment"]
			if appName == "" {
				continue
			}

			var cpu float64
			var mem int64
			for _, container := range pm.Containers {
				cpu += float64(container.Usage.Cpu().MilliValue()) / 1000.0
				mem += container.Usage.Memory().Value()
			}

			entries = append(entries, MetricEntry{
				Ts:        now,
				Pod:       pm.Name,
				Namespace: ns.Name,
				App:       appName,
				Env:       envName,
				CPU:       cpu,
				Memory:    mem,
			})
		}
	}

	if len(entries) > 0 {
		c.liveCache.Add(entries)
		if err := c.store.InsertMetrics(entries); err != nil {
			c.log.Error("failed to insert metrics", "count", len(entries), "error", err)
		} else {
			c.log.Debug("collected metrics", "count", len(entries))
		}
	}
}

// isEnvNamespace checks if a namespace follows the pj-{project}-{env} pattern
// (has at least two hyphens after "pj-").
func isEnvNamespace(name string) bool {
	rest := strings.TrimPrefix(name, "pj-")
	return strings.Contains(rest, "-")
}
