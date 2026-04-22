package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type ObserverServer struct {
	store *Store
	mux   *http.ServeMux
}

func NewObserverServer(store *Store) *ObserverServer {
	s := &ObserverServer{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/logs", s.handleLogs)
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
