package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type trafikAccessLog struct {
	ServiceName           string `json:"ServiceName"`
	OriginStatus          int    `json:"OriginStatus"`
	Duration              int64  `json:"Duration"` // nanoseconds
	RequestContentSize    int64  `json:"RequestContentSize"`
	DownstreamContentSize int64  `json:"DownstreamContentSize"`
}

type appEnvKey struct {
	namespace string
	app       string
	env       string
}

type trafficBucket struct {
	requests  int64
	status2xx int64
	status3xx int64
	status4xx int64
	status5xx int64
	latencies []float64
	bytesIn   int64
	bytesOut  int64
}

type TrafficCollector struct {
	clientset    kubernetes.Interface
	store        *Store
	liveCache    *LiveTrafficCache
	syncInterval time.Duration
	bucketSize   time.Duration
	ingressNs    string
	log          *slog.Logger

	mu      sync.Mutex
	tailers map[string]context.CancelFunc

	accMu       sync.Mutex
	accumulator map[accKey]*trafficBucket

	svcMu    sync.RWMutex
	svcCache map[string]appEnvKey // "{namespace}/{serviceName}" -> appEnvKey
}

type accKey struct {
	appEnvKey
	bucket int64
}

func NewTrafficCollector(cs kubernetes.Interface, store *Store, liveCache *LiveTrafficCache, syncInterval, bucketSize time.Duration, ingressNs string, log *slog.Logger) *TrafficCollector {
	return &TrafficCollector{
		clientset:    cs,
		store:        store,
		liveCache:    liveCache,
		syncInterval: syncInterval,
		bucketSize:   bucketSize,
		ingressNs:    ingressNs,
		log:          log,
		tailers:      make(map[string]context.CancelFunc),
		accumulator:  make(map[accKey]*trafficBucket),
		svcCache:     make(map[string]appEnvKey),
	}
}

func (c *TrafficCollector) Run(ctx context.Context) {
	syncTicker := time.NewTicker(c.syncInterval)
	defer syncTicker.Stop()

	flushTicker := time.NewTicker(c.bucketSize)
	defer flushTicker.Stop()

	c.refreshServiceCache(ctx)
	c.syncTailers(ctx)

	for {
		select {
		case <-ctx.Done():
			c.stopAll()
			return
		case <-syncTicker.C:
			c.refreshServiceCache(ctx)
			c.syncTailers(ctx)
		case <-flushTicker.C:
			c.flush()
		}
	}
}

func (c *TrafficCollector) refreshServiceCache(ctx context.Context) {
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.log.Error("traffic: failed to list namespaces", "error", err)
		return
	}

	cache := make(map[string]appEnvKey)
	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, "pj-") {
			continue
		}
		svcs, err := c.clientset.CoreV1().Services(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name,mortise.dev/environment",
		})
		if err != nil {
			c.log.Warn("traffic: failed to list services", "namespace", ns.Name, "error", err)
			continue
		}
		for _, svc := range svcs.Items {
			key := ns.Name + "-" + svc.Name + "@kubernetes"
			cache[key] = appEnvKey{
				namespace: ns.Name,
				app:       svc.Labels["app.kubernetes.io/name"],
				env:       svc.Labels["mortise.dev/environment"],
			}
		}
	}

	c.svcMu.Lock()
	c.svcCache = cache
	c.svcMu.Unlock()
}

func (c *TrafficCollector) syncTailers(ctx context.Context) {
	pods, err := c.clientset.CoreV1().Pods(c.ingressNs).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=traefik",
	})
	if err != nil {
		c.log.Warn("traffic: failed to list traefik pods", "namespace", c.ingressNs, "error", err)
		return
	}

	active := map[string]bool{}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		key := c.ingressNs + "/" + pod.Name
		active[key] = true

		c.mu.Lock()
		if _, exists := c.tailers[key]; exists {
			c.mu.Unlock()
			continue
		}
		tailCtx, cancel := context.WithCancel(ctx)
		c.tailers[key] = cancel
		c.mu.Unlock()

		go c.tailTraefikPod(tailCtx, pod.Name)
	}

	c.mu.Lock()
	for key, cancel := range c.tailers {
		if !active[key] {
			cancel()
			delete(c.tailers, key)
		}
	}
	c.mu.Unlock()
}

func (c *TrafficCollector) tailTraefikPod(ctx context.Context, podName string) {
	tail := int64(10)
	opts := &corev1.PodLogOptions{
		Follow:    true,
		TailLines: &tail,
	}

	stream, err := c.clientset.CoreV1().Pods(c.ingressNs).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("traffic: failed to open traefik log stream", "pod", podName, "error", err)
		}
		return
	}
	defer stream.Close()

	go func() {
		<-ctx.Done()
		stream.Close()
	}()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		c.processLine(scanner.Bytes())
	}
}

func (c *TrafficCollector) processLine(line []byte) {
	var entry trafikAccessLog
	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}
	if entry.ServiceName == "" || entry.OriginStatus == 0 {
		return
	}

	lookupKey := stripTraefikPort(entry.ServiceName)

	c.svcMu.RLock()
	aek, ok := c.svcCache[lookupKey]
	c.svcMu.RUnlock()
	if !ok {
		return
	}

	bucketSec := int64(c.bucketSize / time.Second)
	if bucketSec <= 0 {
		bucketSec = 5
	}
	now := time.Now().Unix()
	bucket := (now / bucketSec) * bucketSec

	latencyMs := float64(entry.Duration) / 1e6

	c.accMu.Lock()
	key := accKey{appEnvKey: aek, bucket: bucket}
	b := c.accumulator[key]
	if b == nil {
		b = &trafficBucket{}
		c.accumulator[key] = b
	}
	b.requests++
	switch {
	case entry.OriginStatus >= 500:
		b.status5xx++
	case entry.OriginStatus >= 400:
		b.status4xx++
	case entry.OriginStatus >= 300:
		b.status3xx++
	case entry.OriginStatus >= 200:
		b.status2xx++
	}
	b.latencies = append(b.latencies, latencyMs)
	b.bytesIn += entry.RequestContentSize
	b.bytesOut += entry.DownstreamContentSize
	c.accMu.Unlock()
}

func (c *TrafficCollector) flush() {
	bucketSec := int64(c.bucketSize / time.Second)
	if bucketSec <= 0 {
		bucketSec = 5
	}
	now := time.Now().Unix()
	currentBucket := (now / bucketSec) * bucketSec

	c.accMu.Lock()
	var entries []TrafficEntry
	for key, b := range c.accumulator {
		if key.bucket >= currentBucket {
			continue
		}
		entries = append(entries, TrafficEntry{
			Ts:         key.bucket,
			Namespace:  key.namespace,
			App:        key.app,
			Env:        key.env,
			Requests:   b.requests,
			Status2xx:  b.status2xx,
			Status3xx:  b.status3xx,
			Status4xx:  b.status4xx,
			Status5xx:  b.status5xx,
			LatencyP50: percentile(b.latencies, 0.50),
			LatencyP95: percentile(b.latencies, 0.95),
			LatencyP99: percentile(b.latencies, 0.99),
			BytesIn:    b.bytesIn,
			BytesOut:   b.bytesOut,
		})
		delete(c.accumulator, key)
	}
	c.accMu.Unlock()

	if len(entries) == 0 {
		return
	}

	c.liveCache.Add(entries)
	if err := c.store.InsertTraffic(entries); err != nil {
		c.log.Error("traffic: failed to insert", "count", len(entries), "error", err)
	} else {
		c.log.Debug("traffic: flushed", "count", len(entries))
	}
}

func (c *TrafficCollector) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cancel := range c.tailers {
		cancel()
	}
	clear(c.tailers)
}

// stripTraefikPort removes the port segment from Traefik's ServiceName format.
// Traefik emits "{ns}-{svc}-{port}@{provider}", but the cache key is "{ns}-{svc}@{provider}".
func stripTraefikPort(svcName string) string {
	atIdx := strings.Index(svcName, "@")
	if atIdx <= 0 {
		return svcName
	}
	prefix := svcName[:atIdx]
	suffix := svcName[atIdx:]
	lastDash := strings.LastIndex(prefix, "-")
	if lastDash <= 0 {
		return svcName
	}
	candidate := prefix[lastDash+1:]
	for _, c := range candidate {
		if c < '0' || c > '9' {
			return svcName
		}
	}
	return prefix[:lastDash] + suffix
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	idx := p * float64(len(values)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper || upper >= len(values) {
		return values[lower]
	}
	frac := idx - float64(lower)
	return values[lower]*(1-frac) + values[upper]*frac
}
