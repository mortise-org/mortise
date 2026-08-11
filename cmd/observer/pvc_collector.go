package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// kubelet stats/summary shapes — only the fields we read. The Summary API is
// the one interface that reports actual filesystem usage per volume; the
// metrics API has no PVC data at all.
type kubeletSummary struct {
	Pods []kubeletSummaryPod `json:"pods"`
}

type kubeletSummaryPod struct {
	PodRef struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"podRef"`
	Volumes []kubeletVolumeStats `json:"volume"`
}

type kubeletVolumeStats struct {
	Name          string         `json:"name"`
	CapacityBytes int64          `json:"capacityBytes"`
	UsedBytes     int64          `json:"usedBytes"`
	PVCRef        *kubeletPVCRef `json:"pvcRef"`
}

type kubeletPVCRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// PVCCollector samples per-PVC capacity/usage from each node's kubelet
// Summary API (nodes/proxy stats/summary — RBAC justified in the chart).
// Kubelet stats being unavailable (managed clusters that block nodes/proxy,
// kubelets without the endpoint) records a failed heartbeat and produces no
// series — metrics absent, not erroring loudly every tick.
type PVCCollector struct {
	clientset kubernetes.Interface
	store     *Store
	health    *HealthTracker
	prom      *promState
	interval  time.Duration
	log       *slog.Logger
	// summaryFn fetches one node's kubelet summary; a field so tests can
	// inject summaries without a real nodes/proxy endpoint.
	summaryFn func(ctx context.Context, nodeName string) (*kubeletSummary, error)
}

func NewPVCCollector(cs kubernetes.Interface, store *Store, health *HealthTracker, prom *promState, interval time.Duration, log *slog.Logger) *PVCCollector {
	c := &PVCCollector{
		clientset: cs,
		store:     store,
		health:    health,
		prom:      prom,
		interval:  interval,
		log:       log,
	}
	c.summaryFn = c.nodeSummary
	return c
}

func (c *PVCCollector) Run(ctx context.Context) {
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

func (c *PVCCollector) collect(ctx context.Context) {
	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.log.Warn("pvc: failed to list nodes", "error", err)
		c.health.Record(collectorPVC, 0, fmt.Errorf("list nodes: %w", err))
		return
	}

	now := time.Now().Unix()
	var entries []PVCEntry
	var partialErr error

	for _, node := range nodes.Items {
		summary, err := c.summaryFn(ctx, node.Name)
		if err != nil {
			c.log.Debug("pvc: kubelet summary unavailable", "node", node.Name, "error", err)
			if partialErr == nil {
				partialErr = fmt.Errorf("kubelet summary on %s: %w", node.Name, err)
			}
			continue
		}

		for _, pod := range summary.Pods {
			if !strings.HasPrefix(pod.PodRef.Namespace, "pj-") {
				continue
			}
			for _, vol := range pod.Volumes {
				if vol.PVCRef == nil {
					continue
				}
				app, env, project := c.podAppEnv(ctx, pod.PodRef.Namespace, pod.PodRef.Name)
				if app == "" {
					continue
				}
				entries = append(entries, PVCEntry{
					Ts:        now,
					Namespace: pod.PodRef.Namespace,
					App:       app,
					Env:       env,
					PVC:       vol.PVCRef.Name,
					Capacity:  vol.CapacityBytes,
					Used:      vol.UsedBytes,
				})
				c.prom.SetPVC(project, app, env, vol.PVCRef.Name, vol.CapacityBytes, vol.UsedBytes)
			}
		}
	}

	if len(entries) > 0 {
		if err := c.store.InsertPVCMetrics(entries); err != nil {
			c.log.Error("pvc: failed to insert", "count", len(entries), "error", err)
			if partialErr == nil {
				partialErr = fmt.Errorf("insert pvc metrics: %w", err)
			}
		}
	}
	c.health.Record(collectorPVC, len(entries), partialErr)
}

func (c *PVCCollector) nodeSummary(ctx context.Context, nodeName string) (*kubeletSummary, error) {
	raw, err := c.clientset.CoreV1().RESTClient().Get().
		Resource("nodes").Name(nodeName).
		SubResource("proxy", "stats", "summary").
		DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	var summary kubeletSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return nil, fmt.Errorf("decode summary: %w", err)
	}
	return &summary, nil
}

// podAppEnv resolves the owning app/env labels for a pod named in a kubelet
// summary. Cached per collect cycle would be an optimization; at the poll
// interval and pod counts involved a direct Get is fine and always fresh.
func (c *PVCCollector) podAppEnv(ctx context.Context, namespace, podName string) (string, string, string) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", "", ""
	}
	return pod.Labels["app.kubernetes.io/name"], pod.Labels["mortise.dev/environment"], pod.Labels["mortise.dev/project"]
}
