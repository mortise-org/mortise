package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type ObserverServer struct {
	store            *Store
	liveCache        *LiveMetricsCache
	liveTrafficCache *LiveTrafficCache
	mux              *http.ServeMux
}

func NewObserverServer(store *Store, liveCache *LiveMetricsCache, liveTrafficCache *LiveTrafficCache) *ObserverServer {
	s := &ObserverServer{store: store, liveCache: liveCache, liveTrafficCache: liveTrafficCache}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/metrics/live", s.handleMetricsLive)
	mux.HandleFunc("GET /v1/logs", s.handleLogs)
	mux.HandleFunc("GET /v1/traffic", s.handleTraffic)
	mux.HandleFunc("GET /v1/traffic/live", s.handleTrafficLive)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux = mux
	return s
}

func (s *ObserverServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *ObserverServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := q.Get("namespace")
	app := q.Get("app")
	env := q.Get("env")
	startStr := q.Get("start")
	endStr := q.Get("end")

	if namespace == "" || app == "" || env == "" || startStr == "" || endStr == "" {
		writeJSONResp(w, 400, map[string]string{"error": "namespace, app, env, start, end are required"})
		return
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid start"})
		return
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid end"})
		return
	}

	step := int64(60)
	if s := q.Get("step"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			step = n
		}
	}

	pods, err := s.store.QueryMetrics(namespace, app, env, start, end, step)
	if err != nil {
		writeJSONResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]any{"pods": pods})
}

func (s *ObserverServer) handleMetricsLive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := q.Get("namespace")
	app := q.Get("app")
	env := q.Get("env")
	if namespace == "" || app == "" || env == "" {
		writeJSONResp(w, 400, map[string]string{"error": "namespace, app, env are required"})
		return
	}

	windowSec := int64(600)
	if v := q.Get("window"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 10 {
			windowSec = min(n, 86400)
		}
	}
	step := int64(15)
	if v := q.Get("step"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 1 {
			step = n
		}
	}
	interval := 5 * time.Second
	if v := q.Get("interval"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 2 {
			interval = time.Duration(n) * time.Second
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONResp(w, 500, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeSnapshot := func() error {
		now := time.Now().Unix()
		start := now - windowSec
		pods := s.liveCache.Query(namespace, app, env, start, now, step)
		payload := map[string]any{
			"available": true,
			"window":    windowSec,
			"step":      step,
			"ts":        now,
			"pods":      pods,
		}
		b, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "event: metrics\ndata: %s\n\n", b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if writeSnapshot() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if writeSnapshot() != nil {
				return
			}
		}
	}
}

func (s *ObserverServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := q.Get("namespace")
	app := q.Get("app")
	env := q.Get("env")
	startStr := q.Get("start")
	endStr := q.Get("end")

	if namespace == "" || app == "" || env == "" || startStr == "" || endStr == "" {
		writeJSONResp(w, 400, map[string]string{"error": "namespace, app, env, start, end are required"})
		return
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid start"})
		return
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid end"})
		return
	}

	limit := 500
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 2000 {
		limit = 2000
	}

	filter := q.Get("filter")
	before := q.Get("before")

	lines, hasMore, err := s.store.QueryLogs(namespace, app, env, start, end, limit, filter, before)
	if err != nil {
		writeJSONResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]any{"lines": lines, "hasMore": hasMore})
}

func (s *ObserverServer) handleTraffic(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := q.Get("namespace")
	app := q.Get("app")
	env := q.Get("env")
	startStr := q.Get("start")
	endStr := q.Get("end")

	if namespace == "" || app == "" || env == "" || startStr == "" || endStr == "" {
		writeJSONResp(w, 400, map[string]string{"error": "namespace, app, env, start, end are required"})
		return
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid start"})
		return
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		writeJSONResp(w, 400, map[string]string{"error": "invalid end"})
		return
	}

	step := int64(5)
	if v := q.Get("step"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			step = n
		}
	}

	series, err := s.store.QueryTraffic(namespace, app, env, start, end, step)
	if err != nil {
		writeJSONResp(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]any{"series": series})
}

func (s *ObserverServer) handleTrafficLive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespace := q.Get("namespace")
	app := q.Get("app")
	env := q.Get("env")
	if namespace == "" || app == "" || env == "" {
		writeJSONResp(w, 400, map[string]string{"error": "namespace, app, env are required"})
		return
	}

	windowSec := int64(600)
	if v := q.Get("window"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 10 {
			windowSec = min(n, 86400)
		}
	}
	step := int64(5)
	if v := q.Get("step"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 1 {
			step = n
		}
	}
	interval := 5 * time.Second
	if v := q.Get("interval"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 2 {
			interval = time.Duration(n) * time.Second
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONResp(w, 500, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	writeSnapshot := func() error {
		now := time.Now().Unix()
		start := now - windowSec
		series := s.liveTrafficCache.Query(namespace, app, env, start, now, step)
		payload := map[string]any{
			"available": true,
			"window":    windowSec,
			"step":      step,
			"ts":        now,
			"series":    series,
		}
		b, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "event: traffic\ndata: %s\n\n", b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if writeSnapshot() != nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if writeSnapshot() != nil {
				return
			}
		}
	}
}

func (s *ObserverServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.db.Ping(); err != nil {
		writeJSONResp(w, 503, map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}
	writeJSONResp(w, 200, map[string]string{"status": "ok"})
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
