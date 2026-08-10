package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Collector names as recorded in collector_ticks and the health endpoint.
const (
	collectorMetrics = "metrics"
	collectorLogs    = "logs"
	collectorTraffic = "traffic"
	collectorPVC     = "pvc"
)

type MetricsCollector struct {
	clientset     kubernetes.Interface
	metricsClient metricsv.Interface
	store         *Store
	liveCache     *LiveMetricsCache
	health        *HealthTracker
	interval      time.Duration
	log           *slog.Logger
}

func NewMetricsCollector(cs kubernetes.Interface, mc metricsv.Interface, store *Store, liveCache *LiveMetricsCache, health *HealthTracker, interval time.Duration, log *slog.Logger) *MetricsCollector {
	return &MetricsCollector{
		clientset:     cs,
		metricsClient: mc,
		store:         store,
		liveCache:     liveCache,
		health:        health,
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
	// Every cycle records a heartbeat — success (items may legitimately be
	// zero: observed-empty is data), or failure with the cause. A window
	// without heartbeats renders as a gap downstream; it must never be
	// possible to fail out of this function without a tick.
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.log.Error("failed to list namespaces", "error", err)
		c.health.Record(collectorMetrics, 0, fmt.Errorf("list namespaces: %w", err))
		return
	}

	now := time.Now().Unix()
	var entries []MetricEntry
	var partialErr error

	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, "pj-") {
			continue
		}

		podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(ns.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			c.log.Warn("failed to list pod metrics", "namespace", ns.Name, "error", err)
			if partialErr == nil {
				partialErr = fmt.Errorf("list pod metrics in %s: %w", ns.Name, err)
			}
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
			if partialErr == nil {
				partialErr = fmt.Errorf("insert metrics: %w", err)
			}
		} else {
			c.log.Debug("collected metrics", "count", len(entries))
		}
	}
	c.liveCache.Sweep()
	c.health.Record(collectorMetrics, len(entries), partialErr)
}
