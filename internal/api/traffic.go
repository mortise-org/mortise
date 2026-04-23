package api

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/platformconfig"
)

// handleTrafficHistory proxies to the configured traffic adapter.
//
// GET /api/projects/{project}/apps/{app}/traffic?env=production&start=...&end=...&step=5
func (s *Server) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
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
	if cfg.Observability.TrafficAdapterEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	step := r.URL.Query().Get("step")
	if step == "" {
		step = "5"
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

	s.proxyToAdapter(w, r, cfg.Observability.TrafficAdapterEndpoint+"/v1/traffic", cfg.Observability.TrafficAdapterToken, q)
}

// handleTrafficCurrent returns the most recent traffic bucket from the adapter.
//
// GET /api/projects/{project}/apps/{app}/traffic/current?env=production
func (s *Server) handleTrafficCurrent(w http.ResponseWriter, r *http.Request) {
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

	cfg, err := platformconfig.Load(r.Context(), s.client)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	if cfg.Observability.TrafficAdapterEndpoint == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}

	nowTs := time.Now().Unix()
	now := strconv.FormatInt(nowTs, 10)
	start := strconv.FormatInt(nowTs-300, 10)
	envNs := constants.EnvNamespace(projectName, env)
	q := url.Values{
		"namespace": {envNs},
		"app":       {name},
		"env":       {env},
		"start":     {start},
		"end":       {now},
		"step":      {"5"},
	}

	s.proxyToAdapter(w, r, cfg.Observability.TrafficAdapterEndpoint+"/v1/traffic", cfg.Observability.TrafficAdapterToken, q)
}
