package main

import (
	"log/slog"
	"sync"
	"time"
)

// CollectorHealth is the self-health record for one collector: enough for a
// dashboard to say "metrics stale since X" instead of showing a flatline.
type CollectorHealth struct {
	Collector     string `json:"collector"`
	LastTick      int64  `json:"lastTick"`
	LastSuccess   int64  `json:"lastSuccess"`
	LastError     string `json:"lastError,omitempty"`
	LastErrorTime int64  `json:"lastErrorTime,omitempty"`
	ItemsLastTick int    `json:"itemsLastTick"`
}

// HealthTracker records collector ticks both in memory (served by
// /v1/health/collectors) and in the store's collector_ticks table (the
// durable gap-visibility record that survives restarts).
type HealthTracker struct {
	store *Store
	log   *slog.Logger

	mu     sync.Mutex
	status map[string]*CollectorHealth
	gauges map[string]int64
}

func NewHealthTracker(store *Store, log *slog.Logger) *HealthTracker {
	return &HealthTracker{
		store:  store,
		log:    log,
		status: make(map[string]*CollectorHealth),
		gauges: make(map[string]int64),
	}
}

// Record notes one collector cycle. items is the number of series rows the
// cycle produced; zero with err == nil is a valid observed-empty cycle.
func (h *HealthTracker) Record(collector string, items int, err error) {
	now := time.Now().Unix()

	h.mu.Lock()
	st := h.status[collector]
	if st == nil {
		st = &CollectorHealth{Collector: collector}
		h.status[collector] = st
	}
	st.LastTick = now
	st.ItemsLastTick = items
	if err == nil {
		st.LastSuccess = now
	} else {
		st.LastError = err.Error()
		st.LastErrorTime = now
	}
	h.mu.Unlock()

	tick := Tick{Collector: collector, Ts: now, OK: err == nil, Items: items}
	if err != nil {
		tick.Error = err.Error()
	}
	if insertErr := h.store.InsertTick(tick); insertErr != nil {
		h.log.Warn("failed to record collector tick", "collector", collector, "error", insertErr)
	}
}

// SetGauge publishes an auxiliary counter (active tailers, dropped lines).
func (h *HealthTracker) SetGauge(name string, value int64) {
	h.mu.Lock()
	h.gauges[name] = value
	h.mu.Unlock()
}

// AddGauge increments an auxiliary counter.
func (h *HealthTracker) AddGauge(name string, delta int64) {
	h.mu.Lock()
	h.gauges[name] += delta
	h.mu.Unlock()
}

type HealthSnapshot struct {
	Collectors []CollectorHealth `json:"collectors"`
	Gauges     map[string]int64  `json:"gauges"`
}

func (h *HealthTracker) Snapshot() HealthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap := HealthSnapshot{Gauges: make(map[string]int64, len(h.gauges))}
	for _, st := range h.status {
		snap.Collectors = append(snap.Collectors, *st)
	}
	for k, v := range h.gauges {
		snap.Gauges[k] = v
	}
	return snap
}
