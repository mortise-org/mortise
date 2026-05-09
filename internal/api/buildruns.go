package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
)

const (
	buildRunTargetKindLabelName = "mortise.dev/buildrun-target-kind"
	buildRunTargetNameLabelName = "mortise.dev/buildrun-target-name"
)

type buildRunLogsResponse struct {
	Lines     []string `json:"lines"`
	Offset    int      `json:"offset"`
	Building  bool     `json:"building"`
	Timestamp string   `json:"timestamp,omitempty"`
	CommitSHA string   `json:"commitSHA,omitempty"`
	Status    string   `json:"status,omitempty"`
	Error     string   `json:"error"`
}

type buildLogSnapshot struct {
	lines     []string
	building  bool
	timestamp string
	commitSHA string
	status    string
	buildErr  string
}

func buildRunPhaseStatus(phase mortisev1alpha1.BuildRunPhase) string {
	switch phase {
	case mortisev1alpha1.BuildRunPhasePending, mortisev1alpha1.BuildRunPhaseRunning:
		return "Running"
	case mortisev1alpha1.BuildRunPhaseSucceeded:
		return "Succeeded"
	case mortisev1alpha1.BuildRunPhaseFailed:
		return "Failed"
	default:
		return ""
	}
}

func isAppBuildRun(run *mortisev1alpha1.BuildRun, appName string) bool {
	if run == nil {
		return false
	}
	return run.Spec.TargetRef.Kind == mortisev1alpha1.BuildRunTargetAppEnvironment && run.Spec.TargetRef.Name == appName
}

func sortBuildRunsNewestFirst(runs []mortisev1alpha1.BuildRun) {
	sort.SliceStable(runs, func(i, j int) bool {
		ti := buildRunSortTime(&runs[i])
		tj := buildRunSortTime(&runs[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return runs[i].Name < runs[j].Name
	})
}

func sortBuildRunsForApp(app *mortisev1alpha1.App, runs []mortisev1alpha1.BuildRun) {
	if app == nil {
		sortBuildRunsNewestFirst(runs)
		return
	}
	priority := func(name string) int {
		switch name {
		case app.Status.CurrentBuildRunName:
			return 0
		case app.Status.LastBuildRunName:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		pi := priority(runs[i].Name)
		pj := priority(runs[j].Name)
		if pi != pj {
			return pi < pj
		}
		ti := buildRunSortTime(&runs[i])
		tj := buildRunSortTime(&runs[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return runs[i].Name < runs[j].Name
	})
}

func buildRunSortTime(run *mortisev1alpha1.BuildRun) time.Time {
	if run == nil {
		return time.Time{}
	}
	if run.Status.CompletedAt != nil {
		return run.Status.CompletedAt.Time
	}
	if run.Status.FinishedAt != nil {
		return run.Status.FinishedAt.Time
	}
	if run.Status.StartedAt != nil {
		return run.Status.StartedAt.Time
	}
	return run.CreationTimestamp.Time
}

func (s *Server) listAppBuildRuns(ctx context.Context, namespace, appName string) ([]mortisev1alpha1.BuildRun, error) {
	var list mortisev1alpha1.BuildRunList
	if err := s.client.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{
			buildRunTargetKindLabelName: strings.ToLower(mortisev1alpha1.BuildRunTargetAppEnvironment),
			buildRunTargetNameLabelName: appName,
		},
	); err != nil {
		return nil, err
	}
	sortBuildRunsNewestFirst(list.Items)
	return list.Items, nil
}

func (s *Server) getNamedBuildRun(ctx context.Context, namespace, runName string) (*mortisev1alpha1.BuildRun, error) {
	if runName == "" {
		return nil, nil
	}
	var run mortisev1alpha1.BuildRun
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: runName}, &run); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &run, nil
}

func (s *Server) resolveCurrentAppBuildRun(ctx context.Context, namespace string, app *mortisev1alpha1.App) (*mortisev1alpha1.BuildRun, error) {
	if run, err := s.getNamedBuildRun(ctx, namespace, app.Status.CurrentBuildRunName); err != nil {
		return nil, err
	} else if isAppBuildRun(run, app.Name) {
		return run, nil
	}
	runs, err := s.listAppBuildRuns(ctx, namespace, app.Name)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if s.buildLogs != nil && s.buildLogs.GetBuildLogs(buildRunTrackerKey(&runs[i])) != nil {
			return &runs[i], nil
		}
	}
	for i := range runs {
		if runs[i].Status.Phase == mortisev1alpha1.BuildRunPhasePending || runs[i].Status.Phase == mortisev1alpha1.BuildRunPhaseRunning {
			return &runs[i], nil
		}
	}
	return nil, nil
}

func (s *Server) resolveLastAppBuildRun(ctx context.Context, namespace string, app *mortisev1alpha1.App, fallbackRunName string) (*mortisev1alpha1.BuildRun, error) {
	for _, runName := range []string{app.Status.LastBuildRunName, fallbackRunName} {
		if run, err := s.getNamedBuildRun(ctx, namespace, runName); err != nil {
			return nil, err
		} else if isAppBuildRun(run, app.Name) {
			return run, nil
		}
	}
	runs, err := s.listAppBuildRuns(ctx, namespace, app.Name)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}

func (s *Server) getBuildRunForApp(ctx context.Context, namespace, appName string) (*mortisev1alpha1.BuildRun, bool, error) {
	var app mortisev1alpha1.App
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: appName}, &app); err != nil {
		return nil, false, err
	}
	if run, err := s.resolveCurrentAppBuildRun(ctx, namespace, &app); err != nil {
		return nil, false, err
	} else if run != nil {
		return run, true, nil
	}
	run, err := s.resolveLastAppBuildRun(ctx, namespace, &app, "")
	if err != nil {
		return nil, false, err
	}
	return run, false, nil
}

func buildRunTrackerKey(run *mortisev1alpha1.BuildRun) types.NamespacedName {
	return types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
}

func buildRunLogResponseFromConfigMap(cm *corev1.ConfigMap, building bool) buildRunLogsResponse {
	resp := buildRunLogsResponse{
		Lines:    []string{},
		Offset:   0,
		Building: building,
	}
	if cm == nil {
		return resp
	}
	if raw, ok := cm.Data["lines"]; ok && raw != "" {
		resp.Lines = strings.Split(raw, "\n")
	}
	resp.Timestamp = cm.Annotations["mortise.dev/build-timestamp"]
	resp.CommitSHA = cm.Annotations["mortise.dev/build-commit"]
	resp.Status = cm.Annotations["mortise.dev/build-status"]
	if resp.Status == "Failed" {
		resp.Error = cm.Annotations["mortise.dev/build-error"]
	}
	return resp
}

func (s *Server) getLegacyBuildLogsConfigMap(ctx context.Context, namespace, appName string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "buildlogs-" + appName}, &cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &cm, nil
}

func (s *Server) getBuildRunLogsConfigMap(ctx context.Context, run *mortisev1alpha1.BuildRun) (*corev1.ConfigMap, error) {
	if run == nil {
		return nil, nil
	}
	name := ""
	if run.Status.LogRef != nil {
		name = run.Status.LogRef.Name
	}
	if name == "" {
		name = "buildrun-" + run.Name
	}
	var cm corev1.ConfigMap
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: name}, &cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &cm, nil
}

func (s *Server) getBuildRunLogsResponse(ctx context.Context, run *mortisev1alpha1.BuildRun) (buildRunLogsResponse, bool, error) {
	resp := buildRunLogsResponse{
		Lines:     []string{},
		Offset:    0,
		Building:  run != nil && (run.Status.Phase == mortisev1alpha1.BuildRunPhasePending || run.Status.Phase == mortisev1alpha1.BuildRunPhaseRunning),
		CommitSHA: "",
		Status:    "",
	}
	if run == nil {
		return resp, false, nil
	}
	resp.CommitSHA = run.Spec.Revision
	resp.Status = buildRunPhaseStatus(run.Status.Phase)
	if run.Status.Phase == mortisev1alpha1.BuildRunPhaseFailed {
		resp.Error = run.Status.FailureMessage
	}
	cm, err := s.getBuildRunLogsConfigMap(ctx, run)
	if err != nil {
		return resp, false, err
	}
	if cm != nil {
		resp = buildRunLogResponseFromConfigMap(cm, false)
		if resp.CommitSHA == "" {
			resp.CommitSHA = run.Spec.Revision
		}
		if resp.Status == "" {
			resp.Status = buildRunPhaseStatus(run.Status.Phase)
		}
		if resp.Error == "" && run.Status.Phase == mortisev1alpha1.BuildRunPhaseFailed {
			resp.Error = run.Status.FailureMessage
		}
		return resp, true, nil
	}
	if s.buildLogs != nil {
		if lines := s.buildLogs.GetBuildLogs(buildRunTrackerKey(run)); lines != nil {
			resp.Lines = lines
			resp.Building = true
			if resp.Status == "" {
				resp.Status = "Running"
			}
			return resp, true, nil
		}
	}
	if resp.Building {
		return resp, true, nil
	}
	return resp, true, nil
}

func toBuildLogSnapshot(resp buildRunLogsResponse) buildLogSnapshot {
	return buildLogSnapshot{
		lines:     resp.Lines,
		building:  resp.Building,
		timestamp: resp.Timestamp,
		commitSHA: resp.CommitSHA,
		status:    resp.Status,
		buildErr:  resp.Error,
	}
}

func (s *Server) readBuildRunLogSnapshot(ctx context.Context, namespace string, run *mortisev1alpha1.BuildRun) buildLogSnapshot {
	resp, ok, err := s.getBuildRunLogsResponse(ctx, run)
	if err != nil || !ok {
		return buildLogSnapshot{lines: []string{}}
	}
	if !resp.Building && resp.Lines == nil {
		resp.Lines = []string{}
	}
	return toBuildLogSnapshot(resp)
}

// handleListBuildRuns returns durable build executions for an app, newest first.
func (s *Server) handleListBuildRuns(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	appName := chi.URLParam(r, "app")
	runs, err := s.listAppBuildRuns(r.Context(), ns, appName)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: appName}, &app); err == nil {
		sortBuildRunsForApp(&app, runs)
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleGetBuildRun returns a single durable build execution within a project.
func (s *Server) handleGetBuildRun(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	runName := chi.URLParam(r, "name")
	if runName == "" {
		runName = chi.URLParam(r, "run")
	}
	var run mortisev1alpha1.BuildRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: runName}, &run); err != nil {
		writeError(w, r, err)
		return
	}
	appName := run.Spec.TargetRef.Name
	if pathApp := chi.URLParam(r, "app"); pathApp != "" && pathApp != appName {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("build run %q not found", runName)})
		return
	}
	if !isAppBuildRun(&run, appName) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("build run %q not found", runName)})
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	writeJSON(w, http.StatusOK, &run)
}

// handleBuildRunLogs returns build logs for a specific BuildRun.
func (s *Server) handleBuildRunLogs(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	runName := chi.URLParam(r, "name")
	if runName == "" {
		runName = chi.URLParam(r, "run")
	}
	var run mortisev1alpha1.BuildRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: runName}, &run); err != nil {
		writeError(w, r, err)
		return
	}
	appName := run.Spec.TargetRef.Name
	if pathApp := chi.URLParam(r, "app"); pathApp != "" && pathApp != appName {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("build run %q not found", runName)})
		return
	}
	if !isAppBuildRun(&run, appName) {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("build run %q not found", runName)})
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	resp, ok, err := s.getBuildRunLogsResponse(r.Context(), &run)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !ok {
		resp = buildRunLogsResponse{
			Lines:     []string{},
			Offset:    0,
			Building:  false,
			CommitSHA: run.Spec.Revision,
			Status:    buildRunPhaseStatus(run.Status.Phase),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
