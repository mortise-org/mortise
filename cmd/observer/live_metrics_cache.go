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
