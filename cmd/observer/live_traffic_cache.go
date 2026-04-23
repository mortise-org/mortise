package main

import (
	"sort"
	"sync"
	"time"
)

type trafficCacheKey struct {
	namespace string
	app       string
	env       string
}

type LiveTrafficCache struct {
	mu        sync.RWMutex
	retention time.Duration
	series    map[trafficCacheKey][]TrafficEntry
}

func NewLiveTrafficCache(retention time.Duration) *LiveTrafficCache {
	if retention <= 0 {
		retention = 2 * time.Hour
	}
	return &LiveTrafficCache{
		retention: retention,
		series:    make(map[trafficCacheKey][]TrafficEntry),
	}
}

func (c *LiveTrafficCache) Add(entries []TrafficEntry) {
	if c == nil || len(entries) == 0 {
		return
	}

	cutoff := time.Now().Add(-c.retention).Unix()

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range entries {
		key := trafficCacheKey{namespace: e.Namespace, app: e.App, env: e.Env}
		points := c.series[key]
		points = append(points, e)
		points = trimTrafficPoints(points, cutoff)
		c.series[key] = points
	}
}

func (c *LiveTrafficCache) Query(namespace, app, env string, start, end, step int64) *TrafficSeries {
	if c == nil {
		return nil
	}
	if step <= 0 {
		step = 5
	}

	type bucketAcc struct {
		requests   float64
		s2xx       float64
		s3xx       float64
		s4xx       float64
		s5xx       float64
		lp50Sum    float64
		lp95Sum    float64
		lp99Sum    float64
		reqsForLat float64
		bytesIn    float64
		bytesOut   float64
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	key := trafficCacheKey{namespace: namespace, app: app, env: env}
	points, ok := c.series[key]
	if !ok {
		return nil
	}

	buckets := map[int64]*bucketAcc{}
	for _, p := range points {
		if p.Ts < start || p.Ts > end {
			continue
		}
		b := (p.Ts / step) * step
		acc := buckets[b]
		if acc == nil {
			acc = &bucketAcc{}
			buckets[b] = acc
		}
		acc.requests += float64(p.Requests)
		acc.s2xx += float64(p.Status2xx)
		acc.s3xx += float64(p.Status3xx)
		acc.s4xx += float64(p.Status4xx)
		acc.s5xx += float64(p.Status5xx)
		acc.lp50Sum += p.LatencyP50 * float64(p.Requests)
		acc.lp95Sum += p.LatencyP95 * float64(p.Requests)
		acc.lp99Sum += p.LatencyP99 * float64(p.Requests)
		acc.reqsForLat += float64(p.Requests)
		acc.bytesIn += float64(p.BytesIn)
		acc.bytesOut += float64(p.BytesOut)
	}

	sortedBuckets := make([]int64, 0, len(buckets))
	for b := range buckets {
		sortedBuckets = append(sortedBuckets, b)
	}
	sort.Slice(sortedBuckets, func(i, j int) bool { return sortedBuckets[i] < sortedBuckets[j] })

	series := &TrafficSeries{}
	for _, b := range sortedBuckets {
		acc := buckets[b]
		ts := float64(b)
		series.Requests = append(series.Requests, [2]float64{ts, acc.requests})
		series.Status2xx = append(series.Status2xx, [2]float64{ts, acc.s2xx})
		series.Status3xx = append(series.Status3xx, [2]float64{ts, acc.s3xx})
		series.Status4xx = append(series.Status4xx, [2]float64{ts, acc.s4xx})
		series.Status5xx = append(series.Status5xx, [2]float64{ts, acc.s5xx})
		lp50, lp95, lp99 := 0.0, 0.0, 0.0
		if acc.reqsForLat > 0 {
			lp50 = acc.lp50Sum / acc.reqsForLat
			lp95 = acc.lp95Sum / acc.reqsForLat
			lp99 = acc.lp99Sum / acc.reqsForLat
		}
		series.LatencyP50 = append(series.LatencyP50, [2]float64{ts, lp50})
		series.LatencyP95 = append(series.LatencyP95, [2]float64{ts, lp95})
		series.LatencyP99 = append(series.LatencyP99, [2]float64{ts, lp99})
		series.BytesIn = append(series.BytesIn, [2]float64{ts, acc.bytesIn})
		series.BytesOut = append(series.BytesOut, [2]float64{ts, acc.bytesOut})
	}
	return series
}

func (c *LiveTrafficCache) Sweep() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-c.retention).Unix()
	for key, points := range c.series {
		points = trimTrafficPoints(points, cutoff)
		if len(points) == 0 {
			delete(c.series, key)
		} else {
			c.series[key] = points
		}
	}
}

func trimTrafficPoints(points []TrafficEntry, cutoff int64) []TrafficEntry {
	idx := 0
	for idx < len(points) && points[idx].Ts < cutoff {
		idx++
	}
	if idx == 0 {
		return points
	}
	return append([]TrafficEntry(nil), points[idx:]...)
}
