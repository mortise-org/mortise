package main

import (
	"sort"
	"sync"
	"time"
)

type metricPoint struct {
	ts     int64
	cpu    float64
	memory int64
}

type metricsKey struct {
	namespace string
	app       string
	env       string
	pod       string
}

type LiveMetricsCache struct {
	mu        sync.RWMutex
	retention time.Duration
	series    map[metricsKey][]metricPoint
}

func NewLiveMetricsCache(retention time.Duration) *LiveMetricsCache {
	if retention <= 0 {
		retention = 2 * time.Hour
	}
	return &LiveMetricsCache{
		retention: retention,
		series:    make(map[metricsKey][]metricPoint),
	}
}

func (c *LiveMetricsCache) Add(entries []MetricEntry) {
	if c == nil || len(entries) == 0 {
		return
	}

	cutoff := time.Now().Add(-c.retention).Unix()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range entries {
		key := metricsKey{namespace: e.Namespace, app: e.App, env: e.Env, pod: e.Pod}
		points := c.series[key]
		points = append(points, metricPoint{ts: e.Ts, cpu: e.CPU, memory: e.Memory})
		points = trimPoints(points, cutoff)
		c.series[key] = points
	}
}

func (c *LiveMetricsCache) Query(namespace, app, env string, start, end, step int64) []PodMetricsSeries {
	if c == nil {
		return nil
	}
	if step <= 0 {
		step = 1
	}

	type bucketAcc struct {
		cpuSum float64
		memSum float64
		count  int
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	byPod := map[string]map[int64]*bucketAcc{}
	for key, points := range c.series {
		if key.namespace != namespace || key.app != app || key.env != env {
			continue
		}
		for _, p := range points {
			if p.ts < start || p.ts > end {
				continue
			}
			bucket := (p.ts / step) * step
			if byPod[key.pod] == nil {
				byPod[key.pod] = map[int64]*bucketAcc{}
			}
			if byPod[key.pod][bucket] == nil {
				byPod[key.pod][bucket] = &bucketAcc{}
			}
			acc := byPod[key.pod][bucket]
			acc.cpuSum += p.cpu
			acc.memSum += float64(p.memory)
			acc.count++
		}
	}

	podNames := make([]string, 0, len(byPod))
	for pod := range byPod {
		podNames = append(podNames, pod)
	}
	sort.Strings(podNames)

	out := make([]PodMetricsSeries, 0, len(podNames))
	for _, pod := range podNames {
		bucketMap := byPod[pod]
		buckets := make([]int64, 0, len(bucketMap))
		for b := range bucketMap {
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })

		series := PodMetricsSeries{Name: pod}
		for _, b := range buckets {
			acc := bucketMap[b]
			if acc.count == 0 {
				continue
			}
			series.CPU = append(series.CPU, [2]float64{float64(b), acc.cpuSum / float64(acc.count)})
			series.Memory = append(series.Memory, [2]float64{float64(b), acc.memSum / float64(acc.count)})
		}
		out = append(out, series)
	}

	return out
}

// Sweep removes series with no remaining data points.
func (c *LiveMetricsCache) Sweep() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-c.retention).Unix()
	for key, points := range c.series {
		points = trimPoints(points, cutoff)
		if len(points) == 0 {
			delete(c.series, key)
		} else {
			c.series[key] = points
		}
	}
}

func trimPoints(points []metricPoint, cutoff int64) []metricPoint {
	idx := 0
	for idx < len(points) && points[idx].ts < cutoff {
		idx++
	}
	if idx == 0 {
		return points
	}
	return append([]metricPoint(nil), points[idx:]...)
}

// AppUsage is one app-env's latest resource usage summed across its pods,
// as served by /v1/summary for the cluster rollup dashboard.
type AppUsage struct {
	Namespace string  `json:"namespace"`
	App       string  `json:"app"`
	Env       string  `json:"env"`
	CPU       float64 `json:"cpu"`
	Memory    int64   `json:"memory"`
	Pods      int     `json:"pods"`
}

// LatestByApp aggregates each series' most recent point (no older than
// staleCutoff) per app-env: the cluster-wide "what is everything using right
// now" view. Pods whose series went stale simply drop out — absence over
// interpolation, same contract as everything else here.
func (c *LiveMetricsCache) LatestByApp(staleCutoff int64) []AppUsage {
	if c == nil {
		return nil
	}
	type aggKey struct{ namespace, app, env string }
	agg := map[aggKey]*AppUsage{}

	c.mu.RLock()
	for key, points := range c.series {
		if len(points) == 0 {
			continue
		}
		latest := points[len(points)-1]
		if latest.ts < staleCutoff {
			continue
		}
		k := aggKey{key.namespace, key.app, key.env}
		u := agg[k]
		if u == nil {
			u = &AppUsage{Namespace: k.namespace, App: k.app, Env: k.env}
			agg[k] = u
		}
		u.CPU += latest.cpu
		u.Memory += latest.memory
		u.Pods++
	}
	c.mu.RUnlock()

	out := make([]AppUsage, 0, len(agg))
	for _, u := range agg {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out
}
