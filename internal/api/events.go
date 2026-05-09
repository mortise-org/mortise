package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

var appGVR = schema.GroupVersionResource{
	Group:    "mortise.mortise.dev",
	Version:  "v1alpha1",
	Resource: "apps",
}

// handleProjectEvents streams project-level events via Server-Sent Events.
// The client loads initial state via REST, then connects here for live deltas.
//
// GET /api/projects/{project}/events
//
// @Summary Stream project events
// @Description Streams app updates, pod changes, and build logs via SSE for real-time UI updates
// @Tags events
// @Produce text/event-stream
// @Security BearerAuth
// @Param project path string true "Project name"
// @Success 200 {string} string "SSE stream of project events"
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/events [get]
func (s *Server) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writer := &sseWriter{w: w, flusher: flusher}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.watchApps(ctx, ns, writer)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.watchProjectPods(ctx, projectName, writer)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.streamBuildLogs(ctx, ns, writer)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeat(ctx, writer)
	}()

	wg.Wait()
}

// watchApps opens a k8s watch on App CRDs in the project's control namespace
// and emits app.updated / app.deleted SSE events. Reconnects on watch closure.
func (s *Server) watchApps(ctx context.Context, ns string, w *sseWriter) {
	for {
		if ctx.Err() != nil {
			return
		}
		watcher, err := s.dynamicClient.Resource(appGVR).Namespace(ns).Watch(ctx, metav1.ListOptions{})
		if err != nil {
			slog.Warn("app watch failed, retrying", "ns", ns, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		s.drainAppWatch(ctx, watcher, w)
		watcher.Stop()
	}
}

func (s *Server) drainAppWatch(ctx context.Context, watcher watch.Interface, w *sseWriter) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				return
			}
			switch ev.Type {
			case watch.Added, watch.Modified:
				app, err := unstructuredToApp(ev.Object)
				if err != nil {
					continue
				}
				w.writeNamedEvent("app.updated", app)
			case watch.Deleted:
				app, err := unstructuredToApp(ev.Object)
				if err != nil {
					continue
				}
				w.writeNamedEvent("app.deleted", map[string]string{"name": app.Name})
			}
		}
	}
}

func unstructuredToApp(obj runtime.Object) (*mortisev1alpha1.App, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected object type %T", obj)
	}
	data, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}
	var app mortisev1alpha1.App
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// watchProjectPods watches pods across all env namespaces for the project and
// emits "pods" SSE events when the pod list for an app/env changes.
func (s *Server) watchProjectPods(ctx context.Context, projectName string, w *sseWriter) {
	// Discover env namespaces by listing namespaces with the project label.
	// Re-discover periodically to pick up new environments.
	for {
		if ctx.Err() != nil {
			return
		}
		envNamespaces := s.listEnvNamespaces(ctx, projectName)
		if len(envNamespaces) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		// Watch pods in all env namespaces concurrently. Restart the whole
		// batch when any watcher closes (env added/removed).
		podCtx, podCancel := context.WithCancel(ctx)
		var podWg sync.WaitGroup
		for _, envNs := range envNamespaces {
			podWg.Add(1)
			go func(ns string) {
				defer podWg.Done()
				s.watchPodsInNamespace(podCtx, projectName, ns, w)
			}(envNs)
		}

		// Also watch for namespace changes to detect new environments.
		podWg.Add(1)
		go func() {
			defer podWg.Done()
			s.waitForNamespaceChange(podCtx, projectName, len(envNamespaces))
		}()

		podWg.Wait()
		podCancel()
	}
}

func (s *Server) listEnvNamespaces(ctx context.Context, projectName string) []string {
	var nsList corev1.NamespaceList
	if err := s.client.List(ctx, &nsList, client.MatchingLabels{
		constants.ProjectLabel:       projectName,
		constants.NamespaceRoleLabel: constants.NamespaceRoleEnv,
	}); err != nil {
		return nil
	}
	out := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		out = append(out, ns.Name)
	}
	return out
}

// waitForNamespaceChange blocks until the number of env namespaces for the
// project changes, then returns so the caller re-discovers.
func (s *Server) waitForNamespaceChange(ctx context.Context, projectName string, currentCount int) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ns := s.listEnvNamespaces(ctx, projectName)
			if len(ns) != currentCount {
				return
			}
		}
	}
}

func (s *Server) watchPodsInNamespace(ctx context.Context, projectName, ns string, w *sseWriter) {
	sel := fmt.Sprintf("app.kubernetes.io/managed-by=mortise,%s=%s", constants.ProjectLabel, projectName)

	for {
		if ctx.Err() != nil {
			return
		}
		watcher, err := s.clientset.CoreV1().Pods(ns).Watch(ctx, metav1.ListOptions{
			LabelSelector: sel,
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		s.drainPodWatch(ctx, watcher, projectName, ns, w)
		watcher.Stop()
	}
}

func (s *Server) drainPodWatch(ctx context.Context, watcher watch.Interface, projectName, ns string, w *sseWriter) {
	type podKey struct{ app, env string }
	dirty := map[podKey]bool{}
	var debounce *time.Timer

	flush := func() {
		for k := range dirty {
			s.emitPodList(ctx, w, projectName, k.app, k.env, ns)
		}
		dirty = map[podKey]bool{}
	}

	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				if debounce != nil {
					debounce.Stop()
					flush()
				}
				return
			}
			if ev.Type != watch.Added && ev.Type != watch.Modified && ev.Type != watch.Deleted {
				continue
			}
			pod, ok := ev.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			appName := pod.Labels[constants.AppNameLabel]
			envName := pod.Labels[constants.EnvironmentLabel]
			if appName == "" || envName == "" {
				continue
			}
			dirty[podKey{appName, envName}] = true
			if debounce == nil {
				debounce = time.AfterFunc(500*time.Millisecond, func() {
					flush()
					debounce = nil
				})
			} else {
				debounce.Reset(500 * time.Millisecond)
			}
		}
	}
}

func (s *Server) emitPodList(ctx context.Context, w *sseWriter, projectName, appName, envName, ns string) {
	var podList corev1.PodList
	if err := s.client.List(ctx, &podList, client.InNamespace(ns), client.MatchingLabels{
		constants.AppNameLabel:         appName,
		constants.EnvironmentLabel:     envName,
		"app.kubernetes.io/managed-by": "mortise",
	}); err != nil {
		return
	}
	summaries := make([]podSummary, 0, len(podList.Items))
	for i := range podList.Items {
		summaries = append(summaries, summarizePod(&podList.Items[i]))
	}
	w.writeNamedEvent("pods", map[string]any{
		"app":  appName,
		"env":  envName,
		"pods": summaries,
	})
}

// streamBuildLogs polls the in-memory build tracker for any building apps and
// emits build.log SSE events with only new lines since the last tick.
func (s *Server) streamBuildLogs(ctx context.Context, ns string, w *sseWriter) {
	if s.buildLogs == nil {
		<-ctx.Done()
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	type buildStreamState struct {
		runName string
		offset  int
	}

	wasBuilding := map[string]buildStreamState{}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var appList mortisev1alpha1.AppList
			if err := s.client.List(ctx, &appList, client.InNamespace(ns)); err != nil {
				continue
			}

			nowBuilding := map[string]buildStreamState{}
			for i := range appList.Items {
				app := &appList.Items[i]
				if app.Status.Phase != mortisev1alpha1.AppPhaseBuilding {
					if prev, ok := wasBuilding[app.Name]; ok {
						s.emitBuildLogDelta(w, ns, app.Name, prev.runName, false, -1)
					}
					continue
				}
				run, _, err := s.getBuildRunForApp(ctx, ns, app.Name)
				if err != nil || run == nil {
					continue
				}
				prev := wasBuilding[app.Name]
				offset := prev.offset
				if prev.runName != run.Name {
					offset = -1
				}
				nextOffset, emitted := s.emitBuildLogDelta(w, ns, app.Name, run.Name, true, offset)
				if emitted {
					offset = nextOffset
				}
				nowBuilding[app.Name] = buildStreamState{runName: run.Name, offset: offset}
			}
			wasBuilding = nowBuilding
		}
	}
}

func (s *Server) emitBuildLogDelta(w *sseWriter, ns, appName, runName string, building bool, offset int) (int, bool) {
	if !building {
		resp := buildRunLogsResponse{Lines: []string{}, Offset: 0, Building: false}
		if run, err := s.getNamedBuildRun(context.Background(), ns, runName); err == nil && run != nil {
			if runResp, ok, err := s.getBuildRunLogsResponse(context.Background(), run); err == nil && ok {
				resp = runResp
			}
		}
		if len(resp.Lines) == 0 && resp.Status == "" {
			if cm, err := s.getLegacyBuildLogsConfigMap(context.Background(), ns, appName); err == nil && cm != nil {
				resp = buildRunLogResponseFromConfigMap(cm, false)
			}
		}
		w.writeNamedEvent("build.log", map[string]any{
			"app":       appName,
			"lines":     resp.Lines,
			"offset":    0,
			"building":  building,
			"timestamp": resp.Timestamp,
			"commitSHA": resp.CommitSHA,
			"status":    resp.Status,
			"error":     resp.Error,
		})
		return 0, true
	}

	run, err := s.getNamedBuildRun(context.Background(), ns, runName)
	if err != nil || run == nil {
		return offset, false
	}
	key := buildRunTrackerKey(run)
	effectiveOffset := offset
	if effectiveOffset < 0 {
		effectiveOffset = 0
	}
	newLines, total := s.buildLogs.GetBuildLogsSince(key, effectiveOffset)
	if newLines != nil {
		if len(newLines) == 0 {
			if offset >= 0 {
				return total, false
			}
			newLines = []string{}
		}
		w.writeNamedEvent("build.log", map[string]any{
			"app":       appName,
			"lines":     newLines,
			"offset":    effectiveOffset,
			"building":  true,
			"commitSHA": run.Spec.Revision,
			"status":    "Running",
			"error":     "",
		})
		return total, true
	}

	w.writeNamedEvent("build.log", map[string]any{
		"app":       appName,
		"lines":     []string{},
		"offset":    0,
		"building":  true,
		"commitSHA": run.Spec.Revision,
		"status":    "Running",
		"error":     "",
	})
	return 0, true
}

func heartbeat(ctx context.Context, w *sseWriter) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.writeNamedEvent("heartbeat", struct{}{})
		}
	}
}
