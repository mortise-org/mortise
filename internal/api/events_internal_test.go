package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

type stubBuildLogProvider struct {
	lines map[types.NamespacedName][]string
}

func (s *stubBuildLogProvider) GetBuildLogs(key types.NamespacedName) []string {
	lines, ok := s.lines[key]
	if !ok {
		return nil
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

func (s *stubBuildLogProvider) GetBuildLogsSince(key types.NamespacedName, offset int) ([]string, int) {
	lines := s.GetBuildLogs(key)
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

func TestEmitBuildLogDeltaUsesBuildRunTrackerKeyAndPersistedRunLogs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-live",
			Namespace: "pj-default-project",
			Labels: map[string]string{
				buildRunTargetKindLabelName: "appenvironment",
				buildRunTargetNameLabelName: "demo",
			},
		},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind: mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name: "demo",
			},
			Revision: "rev-live",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:  mortisev1alpha1.BuildRunPhaseSucceeded,
			LogRef: &corev1.LocalObjectReference{Name: "buildrun-run-live"},
		},
	}
	logCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildrun-run-live",
			Namespace: run.Namespace,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-05-02T12:00:00Z",
				"mortise.dev/build-commit":    "rev-live",
				"mortise.dev/build-status":    "Succeeded",
			},
		},
		Data: map[string]string{"lines": "final 1\nfinal 2"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, logCM).Build()
	srv := &Server{
		client: c,
		buildLogs: &stubBuildLogProvider{
			lines: map[types.NamespacedName][]string{
				{Namespace: run.Namespace, Name: run.Name}: {"live 1", "live 2"},
			},
		},
	}

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer := &sseWriter{w: rec, flusher: rec}

	nextOffset, emitted := srv.emitBuildLogDelta(writer, run.Namespace, "demo", run.Name, true, -1)
	if !emitted || nextOffset != 2 {
		t.Fatalf("expected live build.log event with offset 2, got emitted=%v offset=%d", emitted, nextOffset)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `event: build.log`) || !strings.Contains(body, `"lines":["live 1","live 2"]`) {
		t.Fatalf("unexpected live SSE payload: %s", body)
	}

	rec.Body.Reset()
	if _, emitted = srv.emitBuildLogDelta(writer, run.Namespace, "demo", run.Name, false, -1); !emitted {
		t.Fatal("expected terminal build.log event")
	}
	body = rec.Body.String()
	if !strings.Contains(body, `"status":"Succeeded"`) || !strings.Contains(body, `"lines":["final 1","final 2"]`) {
		t.Fatalf("unexpected terminal SSE payload: %s", body)
	}
}

func TestReadBuildRunLogSnapshotReturnsEmptyWhenMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "pj-default-project"},
		Spec: mortisev1alpha1.BuildRunSpec{
			TargetRef: mortisev1alpha1.BuildRunTargetRef{Kind: mortisev1alpha1.BuildRunTargetAppEnvironment, Name: "demo"},
		},
		Status: mortisev1alpha1.BuildRunStatus{Phase: mortisev1alpha1.BuildRunPhaseFailed},
	}
	srv := &Server{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()}

	snapshot := srv.readBuildRunLogSnapshot(context.Background(), run.Namespace, run)
	if snapshot.building {
		t.Fatal("expected missing persisted logs to be terminal/non-building")
	}
	if len(snapshot.lines) != 0 {
		t.Fatalf("expected empty lines, got %+v", snapshot.lines)
	}
}

func TestStreamBuildLogsEmitsFinalSnapshotWhenAppPhaseStale(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "pj-default-project"},
		Status: mortisev1alpha1.AppStatus{
			Phase:            mortisev1alpha1.AppPhaseBuilding,
			LastBuildRunName: "run-done",
		},
	}
	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-done",
			Namespace: "pj-default-project",
			Labels: map[string]string{
				buildRunTargetKindLabelName: "appenvironment",
				buildRunTargetNameLabelName: "demo",
			},
		},
		Spec: mortisev1alpha1.BuildRunSpec{
			AppName: "demo",
			TargetRef: mortisev1alpha1.BuildRunTargetRef{
				Kind: mortisev1alpha1.BuildRunTargetAppEnvironment,
				Name: "demo",
			},
			Revision: "rev-done",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase: mortisev1alpha1.BuildRunPhaseSucceeded,
		},
	}

	srv := &Server{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(app, run).Build()}
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer := &sseWriter{w: rec, flusher: rec}

	if _, emitted := srv.emitBuildLogDelta(writer, run.Namespace, "demo", run.Name, false, -1); !emitted {
		t.Fatal("expected terminal build.log event")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `event: build.log`) {
		t.Fatalf("expected build.log event, got %s", body)
	}
	if !strings.Contains(body, `"building":false`) || !strings.Contains(body, `"status":"Succeeded"`) || !strings.Contains(body, `"commitSHA":"rev-done"`) {
		t.Fatalf("unexpected terminal SSE payload: %s", body)
	}
}

func TestReadBuildRunLogSnapshotReturnsTerminalMetadataWithoutPersistedLogs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "pj-default-project"},
		Spec: mortisev1alpha1.BuildRunSpec{
			TargetRef: mortisev1alpha1.BuildRunTargetRef{Kind: mortisev1alpha1.BuildRunTargetAppEnvironment, Name: "demo"},
			Revision:  "rev-done",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:          mortisev1alpha1.BuildRunPhaseFailed,
			FailureMessage: "boom",
		},
	}
	srv := &Server{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()}

	snapshot := srv.readBuildRunLogSnapshot(context.Background(), run.Namespace, run)
	if snapshot.building {
		t.Fatal("expected terminal snapshot")
	}
	if snapshot.commitSHA != "rev-done" || snapshot.status != "Failed" {
		t.Fatalf("expected terminal metadata, got %+v", snapshot)
	}
	if snapshot.buildErr != "boom" {
		t.Fatalf("expected terminal failure message, got %+v", snapshot)
	}
}

func TestReadBuildRunLogSnapshotPreservesFailureMessageWhenPersistedLogOmitsError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "pj-default-project"},
		Spec: mortisev1alpha1.BuildRunSpec{
			TargetRef: mortisev1alpha1.BuildRunTargetRef{Kind: mortisev1alpha1.BuildRunTargetAppEnvironment, Name: "demo"},
			Revision:  "rev-failed",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:          mortisev1alpha1.BuildRunPhaseFailed,
			FailureMessage: "docker build exited 1",
			LogRef:         &corev1.LocalObjectReference{Name: "buildrun-failed"},
		},
	}
	logCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildrun-failed",
			Namespace: run.Namespace,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-05-02T12:00:00Z",
				"mortise.dev/build-commit":    "rev-failed",
				"mortise.dev/build-status":    "Failed",
			},
		},
		Data: map[string]string{"lines": "failed step"},
	}

	srv := &Server{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, logCM).Build()}
	snapshot := srv.readBuildRunLogSnapshot(context.Background(), run.Namespace, run)
	if snapshot.building {
		t.Fatal("expected terminal snapshot")
	}
	if snapshot.status != "Failed" || snapshot.commitSHA != "rev-failed" {
		t.Fatalf("expected failed metadata, got %+v", snapshot)
	}
	if snapshot.buildErr != "docker build exited 1" {
		t.Fatalf("expected failure message fallback, got %+v", snapshot)
	}
}

func TestGetBuildRunLogsResponseReturnsTerminalMetadataWithoutPersistedLogs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := mortisev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add mortise scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	run := &mortisev1alpha1.BuildRun{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "pj-default-project"},
		Spec: mortisev1alpha1.BuildRunSpec{
			TargetRef: mortisev1alpha1.BuildRunTargetRef{Kind: mortisev1alpha1.BuildRunTargetAppEnvironment, Name: "demo"},
			Revision:  "rev-done",
		},
		Status: mortisev1alpha1.BuildRunStatus{
			Phase:          mortisev1alpha1.BuildRunPhaseFailed,
			FailureMessage: "boom",
		},
	}
	srv := &Server{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()}

	resp, ok, err := srv.getBuildRunLogsResponse(context.Background(), run)
	if err != nil {
		t.Fatalf("getBuildRunLogsResponse: %v", err)
	}
	if !ok {
		t.Fatal("expected terminal buildrun response")
	}
	if resp.Building {
		t.Fatalf("expected terminal response, got %+v", resp)
	}
	if resp.CommitSHA != "rev-done" || resp.Status != "Failed" || resp.Error != "boom" {
		t.Fatalf("expected terminal metadata, got %+v", resp)
	}
}
