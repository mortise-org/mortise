package api

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/platformconfig"
)

type podMetricsCurrent struct {
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
}

// handleMetricsCurrent returns real-time CPU/memory from the k8s PodMetrics API.
//
// GET /api/projects/{project}/apps/{app}/metrics/current?env=production
func (s *Server) handleMetricsCurrent(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "app")
	env := envFromQuery(r)

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	if s.metricsClient == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	envNs := constants.EnvNamespace(projectName, env)
	sel := constants.AppNameLabel + "=" + name +
		",app.kubernetes.io/managed-by=mortise" +
		",mortise.dev/environment=" + env

	podMetrics, err := s.metricsClient.PodMetricses(envNs).List(r.Context(), metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	pods := make([]podMetricsCurrent, 0, len(podMetrics.Items))
	for _, pm := range podMetrics.Items {
		var cpu float64
		var mem int64
		for _, c := range pm.Containers {
			cpu += float64(c.Usage.Cpu().MilliValue()) / 1000.0
			mem += c.Usage.Memory().Value()
		}
		pods = append(pods, podMetricsCurrent{Name: pm.Name, CPU: cpu, Memory: mem})
	}

	writeJSON(w, http.StatusOK, map[string]any{"available": true, "pods": pods})
}

// handleMetricsHistory proxies to the configured metrics adapter.
//
// GET /api/projects/{project}/apps/{app}/metrics?env=production&start=...&end=...&step=60
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "app")
	env := envFromQuery(r)

	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	if start == "" || end == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"start and end are required"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	cfg, err := platformconfig.Load(r.Context(), s.client)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	if cfg.Observability.MetricsAdapterEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	step := r.URL.Query().Get("step")
	if step == "" {
		step = "60"
	}

	envNs := constants.EnvNamespace(projectName, env)
	q := url.Values{
		"namespace": {envNs},
		"app":       {name},
		"env":       {env},
		"start":     {start},
		"end":       {end},
		"step":      {step},
	}

	s.proxyToAdapter(w, cfg.Observability.MetricsAdapterEndpoint+"/v1/metrics", cfg.Observability.MetricsAdapterToken, q)
}

// handleLogHistory proxies to the configured logs adapter.
//
// GET /api/projects/{project}/apps/{app}/logs/history?env=production&start=...&end=...&limit=500&filter=error&before=...
func (s *Server) handleLogHistory(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "app")
	env := envFromQuery(r)

	start := r.URL.Query().Get("start")
	end := r.URL.Query().Get("end")
	if start == "" || end == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"start and end are required"})
		return
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	cfg, err := platformconfig.Load(r.Context(), s.client)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	if cfg.Observability.LogsAdapterEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 2000 {
		limit = 2000
	}

	envNs := constants.EnvNamespace(projectName, env)
	q := url.Values{
		"namespace": {envNs},
		"app":       {name},
		"env":       {env},
		"start":     {start},
		"end":       {end},
		"limit":     {strconv.Itoa(limit)},
	}
	if f := r.URL.Query().Get("filter"); f != "" {
		q.Set("filter", f)
	}
	if b := r.URL.Query().Get("before"); b != "" {
		q.Set("before", b)
	}

	s.proxyToAdapter(w, cfg.Observability.LogsAdapterEndpoint+"/v1/logs", cfg.Observability.LogsAdapterToken, q)
}
