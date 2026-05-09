package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

type fakeBuildLogProvider struct {
	lines map[types.NamespacedName][]string
}

func (f *fakeBuildLogProvider) GetBuildLogs(key types.NamespacedName) []string {
	lines, ok := f.lines[key]
	if !ok {
		return nil
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

func (f *fakeBuildLogProvider) GetBuildLogsSince(key types.NamespacedName, offset int) ([]string, int) {
	lines := f.GetBuildLogs(key)
	if lines == nil {
		return nil, 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return []string{}, len(lines)
	}
	return lines[offset:], len(lines)
}

func makeAppBuildRun(namespace, appName, runName, revision string, createdAt time.Time, phase mortisev1alpha1.BuildRunPhase) *mortisev1alpha1.BuildRun {
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              runName,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(createdAt),
			Labels: map[string]string{
				"mortise.dev/buildrun-target-kind": "appenvironment",
				"mortise.dev/buildrun-target-name": appName,
			},
		},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: appName,
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind: mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name: appName,
			},
			Revision: revision,
		},
		Status: mortisev1alpha1.BuildRunStatus{Phase: phase},
	}
	startedAt := metav1.NewTime(createdAt)
	run.Status.StartedAt = &startedAt
	if phase == mortisev1alpha1.BuildRunPhaseSucceeded || phase == mortisev1alpha1.BuildRunPhaseFailed {
		finishedAt := metav1.NewTime(createdAt)
		run.Status.FinishedAt = &finishedAt
		run.Status.CompletedAt = &finishedAt
	}
	return run
}

func TestBuildRunEndpointsListDetailAndLogs(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeGit, Repo: "https://example.com/demo.git"},
		},
		Status: mortisev1alpha1.AppStatus{
			CurrentBuildRunName: "run-new",
			LastBuildRunName:    "run-old",
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "demo", Namespace: ns}, app); err != nil {
		t.Fatalf("get app after create: %v", err)
	}
	app.Status.CurrentBuildRunName = "run-new"
	app.Status.LastBuildRunName = "run-old"
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update app status: %v", err)
	}

	older := makeAppBuildRun(ns, "demo", "run-old", "rev-old", time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), mortisev1alpha1.BuildRunPhaseSucceeded)
	older.Status.LogRef = &corev1.LocalObjectReference{Name: "buildrun-run-old"}
	newer := makeAppBuildRun(ns, "demo", "run-new", "rev-new", time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC), mortisev1alpha1.BuildRunPhaseRunning)
	other := makeAppBuildRun(ns, "other", "run-other", "rev-other", time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC), mortisev1alpha1.BuildRunPhaseSucceeded)

	for _, obj := range []client.Object{older, newer, other} {
		if err := k8sClient.Create(ctx, obj); err != nil {
			t.Fatalf("create buildrun %s: %v", obj.GetName(), err)
		}
	}
	if err := k8sClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildrun-run-old",
			Namespace: ns,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-05-02T10:15:00Z",
				"mortise.dev/build-commit":    "rev-old",
				"mortise.dev/build-status":    "Succeeded",
			},
		},
		Data: map[string]string{"lines": "step 1\nstep 2"},
	}); err != nil {
		t.Fatalf("create buildrun log configmap: %v", err)
	}

	listW := doRequest(h, http.MethodGet, "/api/projects/default/apps/demo/buildruns", nil)
	if listW.Code != http.StatusOK {
		t.Fatalf("list buildruns: expected 200, got %d: %s", listW.Code, listW.Body.String())
	}
	var runs []mortisev1alpha1.BuildRun
	if err := json.NewDecoder(listW.Body).Decode(&runs); err != nil {
		t.Fatalf("decode buildrun list: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 app buildruns, got %d", len(runs))
	}
	gotNames := map[string]bool{runs[0].Name: true, runs[1].Name: true}
	if !gotNames["run-new"] || !gotNames["run-old"] {
		t.Fatalf("expected run-new and run-old in list, got %+v", runs)
	}

	detailW := doRequest(h, http.MethodGet, "/api/projects/default/build-runs/run-old", nil)
	if detailW.Code != http.StatusOK {
		t.Fatalf("get buildrun: expected 200, got %d: %s", detailW.Code, detailW.Body.String())
	}
	var detail mortisev1alpha1.BuildRun
	if err := json.NewDecoder(detailW.Body).Decode(&detail); err != nil {
		t.Fatalf("decode buildrun detail: %v", err)
	}
	if detail.Name != "run-old" || detail.Spec.TargetRef.Name != "demo" {
		t.Fatalf("unexpected buildrun detail: %+v", detail)
	}

	logsW := doRequest(h, http.MethodGet, "/api/projects/default/buildruns/run-old/logs", nil)
	if logsW.Code != http.StatusOK {
		t.Fatalf("get buildrun logs: expected 200, got %d: %s", logsW.Code, logsW.Body.String())
	}
	var logsResp struct {
		Lines     []string `json:"lines"`
		Building  bool     `json:"building"`
		CommitSHA string   `json:"commitSHA"`
		Status    string   `json:"status"`
	}
	if err := json.NewDecoder(logsW.Body).Decode(&logsResp); err != nil {
		t.Fatalf("decode buildrun logs: %v", err)
	}
	if logsResp.Building {
		t.Fatalf("expected terminal buildrun logs, got building=true")
	}
	if logsResp.CommitSHA != "rev-old" || logsResp.Status != "Succeeded" {
		t.Fatalf("unexpected buildrun log metadata: %+v", logsResp)
	}
	if len(logsResp.Lines) != 2 || logsResp.Lines[0] != "step 1" {
		t.Fatalf("unexpected buildrun log lines: %+v", logsResp.Lines)
	}
}

func TestBuildLogsUsesCurrentBuildRunTrackerAndPersistedRunLogs(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeGit, Repo: "https://example.com/demo.git"},
		},
		Status: mortisev1alpha1.AppStatus{
			Phase:               mortisev1alpha1.AppPhaseBuilding,
			CurrentBuildRunName: "run-live",
		},
	}
	liveRun := makeAppBuildRun(ns, "demo", "run-live", "rev-live", time.Now().UTC(), mortisev1alpha1.BuildRunPhaseRunning)
	doneRun := makeAppBuildRun(ns, "demo", "run-done", "rev-done", time.Now().UTC().Add(-time.Hour), mortisev1alpha1.BuildRunPhaseSucceeded)
	doneRun.Status.LogRef = &corev1.LocalObjectReference{Name: "buildrun-run-done"}

	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "demo", Namespace: ns}, app); err != nil {
		t.Fatalf("get app after create: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseBuilding
	app.Status.CurrentBuildRunName = "run-live"
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update app status: %v", err)
	}
	for _, run := range []*mortisev1alpha1.BuildRun{liveRun, doneRun} {
		if err := k8sClient.Create(ctx, run); err != nil {
			t.Fatalf("create buildrun %s: %v", run.Name, err)
		}
	}
	if err := k8sClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildrun-run-done",
			Namespace: ns,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-05-02T11:00:00Z",
				"mortise.dev/build-commit":    "rev-done",
				"mortise.dev/build-status":    "Succeeded",
			},
		},
		Data: map[string]string{"lines": "final 1\nfinal 2"},
	}); err != nil {
		t.Fatalf("create persisted buildrun log configmap: %v", err)
	}

	srv.SetBuildLogProvider(&fakeBuildLogProvider{
		lines: map[types.NamespacedName][]string{
			{Namespace: ns, Name: "run-live"}: {"live 1", "live 2"},
		},
	})

	liveW := doRequest(h, http.MethodGet, "/api/projects/default/apps/demo/build-logs", nil)
	if liveW.Code != http.StatusOK {
		t.Fatalf("live build-logs: expected 200, got %d: %s", liveW.Code, liveW.Body.String())
	}
	var liveResp struct {
		Lines     []string `json:"lines"`
		Building  bool     `json:"building"`
		CommitSHA string   `json:"commitSHA"`
		Status    string   `json:"status"`
	}
	if err := json.NewDecoder(liveW.Body).Decode(&liveResp); err != nil {
		t.Fatalf("decode live build-logs: %v", err)
	}
	if !liveResp.Building || liveResp.CommitSHA != "rev-live" || liveResp.Status != "Running" {
		t.Fatalf("unexpected live build-logs response: %+v", liveResp)
	}
	if len(liveResp.Lines) != 2 || liveResp.Lines[0] != "live 1" {
		t.Fatalf("unexpected live build-logs lines: %+v", liveResp.Lines)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "demo", Namespace: ns}, app); err != nil {
		t.Fatalf("get app: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseReady
	app.Status.CurrentBuildRunName = ""
	app.Status.LastBuildRunName = "run-done"
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update app terminal status: %v", err)
	}

	terminalW := doRequest(h, http.MethodGet, "/api/projects/default/apps/demo/build-logs", nil)
	if terminalW.Code != http.StatusOK {
		t.Fatalf("terminal build-logs: expected 200, got %d: %s", terminalW.Code, terminalW.Body.String())
	}
	var terminalResp struct {
		Lines     []string `json:"lines"`
		Building  bool     `json:"building"`
		CommitSHA string   `json:"commitSHA"`
		Status    string   `json:"status"`
	}
	if err := json.NewDecoder(terminalW.Body).Decode(&terminalResp); err != nil {
		t.Fatalf("decode terminal build-logs: %v", err)
	}
	if terminalResp.Building || terminalResp.CommitSHA != "rev-done" || terminalResp.Status != "Succeeded" {
		t.Fatalf("unexpected terminal build-logs response: %+v", terminalResp)
	}
	if len(terminalResp.Lines) != 2 || terminalResp.Lines[0] != "final 1" {
		t.Fatalf("unexpected terminal build-logs lines: %+v", terminalResp.Lines)
	}
}

func TestBuildLogsPrefersCurrentBuildRunWhenPhaseStale(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	appName := "demo-stale"
	liveRunName := "run-live-stale"
	doneRunName := "run-done-stale"

	ctx := context.Background()
	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeGit, Repo: "https://example.com/demo.git"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: appName, Namespace: ns}, app); err != nil {
		t.Fatalf("get app after create: %v", err)
	}
	app.Status.Phase = mortisev1alpha1.AppPhaseReady
	app.Status.CurrentBuildRunName = liveRunName
	app.Status.LastBuildRunName = doneRunName
	if err := k8sClient.Status().Update(ctx, app); err != nil {
		t.Fatalf("update app status: %v", err)
	}

	liveRun := makeAppBuildRun(ns, appName, liveRunName, "rev-live", time.Now().UTC(), mortisev1alpha1.BuildRunPhaseRunning)
	doneRun := makeAppBuildRun(ns, appName, doneRunName, "rev-done", time.Now().UTC().Add(-time.Hour), mortisev1alpha1.BuildRunPhaseSucceeded)
	doneRun.Status.LogRef = &corev1.LocalObjectReference{Name: "buildrun-" + doneRunName}
	for _, run := range []*mortisev1alpha1.BuildRun{liveRun, doneRun} {
		if err := k8sClient.Create(ctx, run); err != nil {
			t.Fatalf("create buildrun %s: %v", run.Name, err)
		}
		if err := k8sClient.Status().Update(ctx, run); err != nil {
			t.Fatalf("update buildrun %s status: %v", run.Name, err)
		}
	}
	if err := k8sClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildrun-" + doneRunName,
			Namespace: ns,
			Annotations: map[string]string{
				"mortise.dev/build-commit": "rev-done",
				"mortise.dev/build-status": "Succeeded",
			},
		},
		Data: map[string]string{"lines": "done line"},
	}); err != nil {
		t.Fatalf("create persisted terminal log configmap: %v", err)
	}

	srv.SetBuildLogProvider(&fakeBuildLogProvider{
		lines: map[types.NamespacedName][]string{
			{Namespace: ns, Name: liveRunName}: {"live 1", "live 2"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/"+appName+"/build-logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("build-logs: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Lines     []string `json:"lines"`
		Building  bool     `json:"building"`
		CommitSHA string   `json:"commitSHA"`
		Status    string   `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode build-logs: %v", err)
	}
	if !resp.Building || resp.CommitSHA != "rev-live" || resp.Status != "Running" {
		t.Fatalf("unexpected build-logs response: %+v", resp)
	}
	if len(resp.Lines) != 2 || resp.Lines[0] != "live 1" {
		t.Fatalf("unexpected live lines: %+v", resp.Lines)
	}
}
