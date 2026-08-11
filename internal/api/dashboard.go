package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/platformconfig"
)

// The dashboard payload (obs-v2 O5): one aggregate request instead of the
// client fanning out per project. Authorization is applied server-side:
// members see only their readable projects' rows and counts; the
// cluster-level strip fields (node capacity, observer health) are
// admin-only and omitted otherwise.

type dashboardEnvPhase struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
}

type dashboardApp struct {
	Project  string              `json:"project"`
	Name     string              `json:"name"`
	Phase    string              `json:"phase"`
	Envs     []dashboardEnvPhase `json:"envs"`
	CPU      float64             `json:"cpu"`
	Memory   int64               `json:"memory"`
	Restarts int64               `json:"restarts"`
	// MetricsAvailable distinguishes "using zero" from "usage unknown"
	// (adapter absent or this app's series missing/stale).
	MetricsAvailable bool `json:"metricsAvailable"`
}

type dashboardProject struct {
	Name      string               `json:"name"`
	AppCount  int                  `json:"appCount"`
	EnvHealth map[string]EnvHealth `json:"envHealth"`
}

type dashboardCollector struct {
	Collector   string `json:"collector"`
	OK          bool   `json:"ok"`
	LastSuccess int64  `json:"lastSuccess"`
}

type dashboardCluster struct {
	Projects      int            `json:"projects"`
	Apps          int            `json:"apps"`
	AppsByPhase   map[string]int `json:"appsByPhase"`
	BuildsRunning int            `json:"buildsRunning"`
	BuildsQueued  int            `json:"buildsQueued"`
	// Usage totals cover the caller's readable scope, so a member's strip
	// is consistent with their table.
	CPUUsed          float64 `json:"cpuUsed"`
	MemoryUsed       int64   `json:"memoryUsed"`
	MetricsAvailable bool    `json:"metricsAvailable"`
	// Admin-only (omitted otherwise): cluster capacity and observer health.
	CPUAllocatable    *float64             `json:"cpuAllocatable,omitempty"`
	MemoryAllocatable *int64               `json:"memoryAllocatable,omitempty"`
	Observer          []dashboardCollector `json:"observer,omitempty"`
}

type dashboardResponse struct {
	Cluster  dashboardCluster   `json:"cluster"`
	Projects []dashboardProject `json:"projects"`
	Apps     []dashboardApp     `json:"apps"`
}

// adapterAppUsage mirrors the observer's /v1/summary entry.
type adapterAppUsage struct {
	Namespace string  `json:"namespace"`
	App       string  `json:"app"`
	Env       string  `json:"env"`
	CPU       float64 `json:"cpu"`
	Memory    int64   `json:"memory"`
}

// Dashboard returns the per-cluster rollup.
//
// GET /api/dashboard
//
// @Summary Cluster rollup dashboard
// @Description Returns the apps table, per-project environment health, and cluster strip for the caller's readable projects. Node capacity and observer health are included for admins only.
// @Tags dashboard
// @Produce json
// @Security BearerAuth
// @Success 200 {object} dashboardResponse
// @Failure 403 {object} errorResponse
// @Router /dashboard [get]
func (s *Server) Dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, authz.Resource{Kind: "project"}, authz.ActionRead) {
		return
	}

	readable, err := s.readableProjects(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	readableSet := make(map[string]bool, len(readable))
	for _, p := range readable {
		readableSet[p] = true
	}

	var projectList mortisev1alpha1.ProjectList
	if err := s.client.List(r.Context(), &projectList); err != nil {
		writeError(w, r, err)
		return
	}

	usage, usageOK := s.dashboardUsage(r.Context())
	restarts := s.dashboardRestarts(r.Context())

	resp := dashboardResponse{
		Cluster: dashboardCluster{AppsByPhase: map[string]int{}, MetricsAvailable: usageOK},
	}

	for i := range projectList.Items {
		project := &projectList.Items[i]
		if !readableSet[project.Name] {
			continue
		}
		controlNs := constants.ControlNamespace(project.Name)
		var apps mortisev1alpha1.AppList
		if err := s.client.List(r.Context(), &apps, client.InNamespace(controlNs)); err != nil {
			writeError(w, r, err)
			return
		}

		dp := dashboardProject{Name: project.Name, AppCount: len(apps.Items), EnvHealth: map[string]EnvHealth{}}
		for _, env := range project.Spec.Environments {
			dp.EnvHealth[env.Name] = aggregateEnvHealth(env.Name, apps.Items)
		}
		resp.Projects = append(resp.Projects, dp)
		resp.Cluster.Projects++

		for j := range apps.Items {
			app := &apps.Items[j]
			phase := string(app.Status.Phase)
			if phase == "" {
				phase = string(mortisev1alpha1.AppPhasePending)
			}
			row := dashboardApp{Project: project.Name, Name: app.Name, Phase: phase}
			for _, env := range project.Spec.Environments {
				if !appParticipatesInEnv(app, env.Name) {
					continue
				}
				row.Envs = append(row.Envs, dashboardEnvPhase{
					Name:  env.Name,
					Phase: string(phaseForEnv(app, env.Name)),
				})
				if u, ok := usage[usageKey{constants.EnvNamespace(project.Name, env.Name), app.Name}]; ok {
					row.CPU += u.CPU
					row.Memory += u.Memory
					row.MetricsAvailable = true
				}
				row.Restarts += restarts[usageKey{constants.EnvNamespace(project.Name, env.Name), app.Name}]
			}
			resp.Cluster.Apps++
			resp.Cluster.AppsByPhase[phase]++
			resp.Cluster.CPUUsed += row.CPU
			resp.Cluster.MemoryUsed += row.Memory
			resp.Apps = append(resp.Apps, row)
		}
	}

	s.dashboardBuildCounts(r.Context(), readableSet, &resp.Cluster)

	if p := PrincipalFromContext(r.Context()); p != nil && p.Role == auth.RoleAdmin {
		s.dashboardAdminFields(r.Context(), &resp.Cluster)
	}

	writeJSON(w, http.StatusOK, resp)
}

type usageKey struct{ namespace, app string }

// dashboardUsage fetches the observer's cluster-wide latest-usage summary.
// Absent adapter or a failed fetch degrades to metrics-unavailable — the
// dashboard renders counts and health without usage rather than erroring.
func (s *Server) dashboardUsage(ctx context.Context) (map[usageKey]adapterAppUsage, bool) {
	cfg, err := platformconfig.Load(ctx, s.client)
	if err != nil || cfg.Observability.MetricsAdapterEndpoint == "" {
		return nil, false
	}
	base := strings.TrimSuffix(cfg.Observability.MetricsAdapterEndpoint, "/")
	u, err := url.Parse(base + "/v1/summary")
	if err != nil {
		return nil, false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	if cfg.Observability.MetricsAdapterToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Observability.MetricsAdapterToken)
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return nil, false
	}
	var body struct {
		Apps []adapterAppUsage `json:"apps"`
	}
	if err := json.NewDecoder(io.LimitReader(httpResp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, false
	}
	out := make(map[usageKey]adapterAppUsage, len(body.Apps))
	for _, a := range body.Apps {
		key := usageKey{a.Namespace, a.App}
		agg := out[key]
		agg.CPU += a.CPU
		agg.Memory += a.Memory
		out[key] = agg
	}
	return out, true
}

// dashboardRestarts sums current container restart counts per app from pod
// status, one cluster-wide list over Mortise-managed pods (the operator
// already holds cluster-wide pod read for logs/exec).
func (s *Server) dashboardRestarts(ctx context.Context) map[usageKey]int64 {
	out := map[usageKey]int64{}
	if s.clientset == nil {
		return out
	}
	pods, err := s.clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", constants.ManagedByLabel, constants.ManagedByValue),
	})
	if err != nil {
		return out
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		appName := pod.Labels[constants.AppNameLabel]
		if appName == "" {
			continue
		}
		var n int64
		for _, cs := range pod.Status.ContainerStatuses {
			n += int64(cs.RestartCount)
		}
		out[usageKey{pod.Namespace, appName}] += n
	}
	return out
}

// dashboardBuildCounts counts non-terminal BuildRuns in the caller's
// readable control namespaces: Running = building now, Pending = queued.
func (s *Server) dashboardBuildCounts(ctx context.Context, readable map[string]bool, cluster *dashboardCluster) {
	var runs mortisev1alpha1.BuildRunList
	if err := s.client.List(ctx, &runs); err != nil {
		return
	}
	for i := range runs.Items {
		br := &runs.Items[i]
		project, ok := constants.ProjectFromControlNs(br.Namespace)
		if !ok || !readable[project] {
			continue
		}
		switch br.Status.Phase {
		case mortisev1alpha1.BuildRunPhaseRunning:
			cluster.BuildsRunning++
		case mortisev1alpha1.BuildRunPhasePending, "":
			cluster.BuildsQueued++
		}
	}
}

// dashboardAdminFields fills the admin-only strip fields; each degrades
// independently (nil / omitted) when its source is unavailable.
func (s *Server) dashboardAdminFields(ctx context.Context, cluster *dashboardCluster) {
	if s.clientset != nil {
		if nodes, err := s.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
			var cpu float64
			var mem int64
			for i := range nodes.Items {
				alloc := nodes.Items[i].Status.Allocatable
				cpu += alloc.Cpu().AsApproximateFloat64()
				mem += alloc.Memory().Value()
			}
			cluster.CPUAllocatable = &cpu
			cluster.MemoryAllocatable = &mem
		}
	}

	cfg, err := platformconfig.Load(ctx, s.client)
	if err != nil || cfg.Observability.MetricsAdapterEndpoint == "" {
		return
	}
	base := strings.TrimSuffix(cfg.Observability.MetricsAdapterEndpoint, "/")
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/v1/health/collectors", nil)
	if err != nil {
		return
	}
	if cfg.Observability.MetricsAdapterToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Observability.MetricsAdapterToken)
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return
	}
	var body struct {
		Collectors []struct {
			Collector   string `json:"collector"`
			LastTick    int64  `json:"lastTick"`
			LastSuccess int64  `json:"lastSuccess"`
		} `json:"collectors"`
	}
	if err := json.NewDecoder(io.LimitReader(httpResp.Body, 1<<20)).Decode(&body); err != nil {
		return
	}
	for _, c := range body.Collectors {
		cluster.Observer = append(cluster.Observer, dashboardCollector{
			Collector:   c.Collector,
			OK:          c.LastTick > 0 && c.LastTick == c.LastSuccess,
			LastSuccess: c.LastSuccess,
		})
	}
}
