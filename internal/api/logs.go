package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

type logLine struct {
	Pod    string `json:"pod"`
	Ts     string `json:"ts,omitempty"`
	Line   string `json:"line"`
	Stream string `json:"stream"`
}

// parseLogLine splits a kubelet log line of the form
// "<RFC3339Nano> <content>" into its timestamp and content parts. If the
// prefix is absent or doesn't parse, returns ("", raw) so the caller can emit
// the line unmodified.
func parseLogLine(raw string) (ts, content string) {
	idx := strings.IndexByte(raw, ' ')
	if idx <= 0 {
		return "", raw
	}
	if _, err := time.Parse(time.RFC3339Nano, raw[:idx]); err != nil {
		return "", raw
	}
	return raw[:idx], raw[idx+1:]
}

// handleBuildLogs returns build log lines for an App. While a build is in
// flight, lines come from the operator's in-memory build tracker; once the
// build finishes, the final buffer is persisted to a ConfigMap
// (`buildlogs-{app}`) by the controller and served from there on subsequent
// requests so operator restarts don't drop the most recent log.
//
// GET /api/projects/{project}/apps/{app}/build-logs
//
// @Summary Get build logs for an app
// @Description Returns build log lines from the in-memory tracker (if building) or from the persisted ConfigMap
// @Tags logs
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Success 200 {object} map[string]any "Build log lines with metadata"
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/build-logs [get]
func (s *Server) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionRead) {
		return
	}
	ns, _, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "app")
	key := types.NamespacedName{Namespace: ns, Name: name}

	// In-flight build: serve from the in-memory tracker.
	if s.buildLogs != nil {
		if lines := s.buildLogs.GetBuildLogs(key); lines != nil {
			writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "offset": 0, "building": true})
			return
		}
	}

	// Fallback: load the persisted ConfigMap from the last completed build.
	var cm corev1.ConfigMap
	cmKey := types.NamespacedName{Namespace: ns, Name: "buildlogs-" + name}
	if err := s.client.Get(r.Context(), cmKey, &cm); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"lines": []string{}, "offset": 0, "building": false})
		return
	}

	lines := []string{}
	if raw, ok := cm.Data["lines"]; ok && raw != "" {
		lines = strings.Split(raw, "\n")
	}

	resp := map[string]any{
		"lines":     lines,
		"offset":    0,
		"building":  false,
		"timestamp": cm.Annotations["mortise.dev/build-timestamp"],
		"commitSHA": cm.Annotations["mortise.dev/build-commit"],
		"status":    cm.Annotations["mortise.dev/build-status"],
		"error":     "",
	}
	if cm.Annotations["mortise.dev/build-status"] == "Failed" {
		resp["error"] = cm.Annotations["mortise.dev/build-error"]
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLogs streams pod logs for an App environment via Server-Sent Events.
// All pods matching the Deployment's label selector are aggregated onto the
// single response; each line is tagged with its pod name. New pods created
// during the stream (e.g. rollouts) are picked up via a pod watcher and their
// logs are joined into the stream.
//
// @Summary Stream app logs
// @Description Streams pod logs via SSE, aggregating all pods for the app environment
// @Tags logs
// @Produce text/event-stream
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param env query string false "Environment name (default: production)"
// @Param follow query boolean false "Follow log stream"
// @Param previous query boolean false "Return logs from previous container instance"
// @Param pod query string false "Pin to a specific pod name"
// @Param tail query integer false "Number of tail lines (default: 100)"
// @Param sinceSeconds query integer false "Seconds ago to start from"
// @Param sinceTime query string false "RFC3339 timestamp to start from"
// @Success 200 {string} string "SSE stream of logLine objects"
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Router /projects/{project}/apps/{app}/logs [get]
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
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
	follow := r.URL.Query().Get("follow") == "true"
	previous := r.URL.Query().Get("previous") == "true"
	pinnedPod := r.URL.Query().Get("pod")

	tailLines := int64(100)
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil && n >= 0 {
			tailLines = n
		}
	}

	var sinceSeconds *int64
	if s := r.URL.Query().Get("sinceSeconds"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			sinceSeconds = &n
		}
	}
	var sinceTime *metav1.Time
	if ts := r.URL.Query().Get("sinceTime"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			mt := metav1.NewTime(t)
			sinceTime = &mt
		}
	}
	// If both are set, prefer sinceTime and drop sinceSeconds.
	if sinceTime != nil {
		sinceSeconds = nil
	}

	// Resolve the App CRD (404 if missing). CRD lives in the control ns.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	// Workload pods live in the per-env namespace.
	envNs := constants.EnvNamespace(projectName, env)

	selSet := map[string]string{
		constants.AppNameLabel:         name,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	}
	sel := labels.SelectorFromSet(selSet)

	opts := podLogOpts{
		tailLines:    tailLines,
		follow:       follow,
		previous:     previous,
		sinceSeconds: sinceSeconds,
		sinceTime:    sinceTime,
	}

	// Pinned-pod mode: stream a single named pod after verifying it carries
	// the right labels. No label-selector list, no watcher.
	if pinnedPod != "" {
		var pod corev1.Pod
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: pinnedPod, Namespace: envNs}, &pod); err != nil {
			writeError(w, err)
			return
		}
		if pod.Labels[constants.AppNameLabel] != name || pod.Labels["mortise.dev/environment"] != env {
			writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("pod %q does not belong to app %q env %q", pinnedPod, name, env)})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, errorResponse{"streaming not supported"})
			return
		}

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		writer := &sseWriter{w: w, flusher: flusher}
		s.streamPodLogs(ctx, writer, envNs, pinnedPod, opts)
		return
	}

	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList, client.InNamespace(envNs), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	if len(podList.Items) == 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("no pods found for app %q env %q", name, env)})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"streaming not supported"})
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writer := &sseWriter{w: w, flusher: flusher}

	var wg sync.WaitGroup
	started := newPodTracker()

	streamPod := func(pod *corev1.Pod) {
		if pod == nil {
			return
		}
		podCtx, podCancel := context.WithCancel(ctx)
		ok, oldCancel := started.add(pod.Name, podRestartGeneration(pod), podCancel)
		if !ok {
			podCancel()
			return
		}
		if oldCancel != nil {
			oldCancel()
			writer.writeEvent(logLine{Pod: pod.Name, Line: "[pod restarted — reconnecting]", Stream: "stderr"})
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.streamPodLogs(podCtx, writer, envNs, pod.Name, opts)
		}()
	}

	for _, p := range podList.Items {
		pod := p
		streamPod(&pod)
	}

	// If follow=true, watch for new pods that match the selector (e.g. rollouts)
	// and start streaming them too.
	if follow {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchPods(ctx, envNs, sel.String(), streamPod)
		}()
	}

	wg.Wait()
}

// podLogOpts bundles the query-param-derived options that control a single
// pod's log stream so they can be plumbed through without a growing argument
// list.
type podLogOpts struct {
	tailLines    int64
	follow       bool
	previous     bool
	sinceSeconds *int64
	sinceTime    *metav1.Time
}

// streamPodLogs streams logs from a single pod onto the shared SSE writer.
// Exits cleanly when the pod terminates, the context is cancelled, or the
// log stream returns EOF.
func (s *Server) streamPodLogs(ctx context.Context, w *sseWriter, ns, podName string, o podLogOpts) {
	tail := o.tailLines
	opts := &corev1.PodLogOptions{
		Follow:       o.follow,
		TailLines:    &tail,
		Previous:     o.previous,
		SinceSeconds: o.sinceSeconds,
		SinceTime:    o.sinceTime,
		Timestamps:   true,
	}

	stream, err := s.clientset.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.writeEvent(logLine{Pod: podName, Line: fmt.Sprintf("error opening log stream: %v", err), Stream: "stderr"})
		}
		return
	}
	defer func() { _ = stream.Close() }()

	// Close stream when context is cancelled so the scanner unblocks.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		ts, content := parseLogLine(scanner.Text())
		w.writeEvent(logLine{Pod: podName, Ts: ts, Line: content, Stream: "stdout"})
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil && err != io.EOF {
		w.writeEvent(logLine{Pod: podName, Line: fmt.Sprintf("stream ended: %v", err), Stream: "stderr"})
	}
}

// watchPods uses the clientset to watch for new pods matching the label
// selector and invokes onAdd whenever a pod is added or modified into a
// state worth streaming. Returns when ctx is cancelled.
func (s *Server) watchPods(ctx context.Context, ns, labelSelector string, onAdd func(*corev1.Pod)) {
	watcher, err := s.clientset.CoreV1().Pods(ns).Watch(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				return
			}
			if ev.Type != watch.Added && ev.Type != watch.Modified {
				continue
			}
			pod, ok := ev.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			onAdd(pod)
		}
	}
}

// sseWriter serializes writes to the HTTP response so concurrent per-pod
// goroutines don't interleave SSE events.
type sseWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s *sseWriter) writeEvent(line logLine) {
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", data)
	s.flusher.Flush()
}

// writeNamedEvent writes a typed SSE event with an "event:" field so the
// client can dispatch via EventSource.addEventListener(eventType, ...).
func (s *sseWriter) writeNamedEvent(eventType string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, raw)
	s.flusher.Flush()
}

// podTracker records which pod names have already had a streaming goroutine
// started so a pod watcher re-reporting an existing pod doesn't spawn
// duplicate log streams. On restart, the old goroutine's cancel func is
// returned so the caller can stop it before starting the replacement.
type podTracker struct {
	mu   sync.Mutex
	seen map[string]podEntry
}

type podEntry struct {
	restartGen int32
	cancel     context.CancelFunc
}

func newPodTracker() *podTracker {
	return &podTracker{seen: map[string]podEntry{}}
}

// add registers a pod. Returns (true, oldCancel) if a new stream should start.
// oldCancel is non-nil when the pod restarted and the previous stream should
// be cancelled.
func (p *podTracker) add(name string, restartGen int32, cancel context.CancelFunc) (bool, context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev, ok := p.seen[name]; ok && restartGen <= prev.restartGen {
		return false, nil
	}
	old := p.seen[name]
	p.seen[name] = podEntry{restartGen: restartGen, cancel: cancel}
	return true, old.cancel
}

func podRestartGeneration(pod *corev1.Pod) int32 {
	if pod == nil {
		return 0
	}
	var gen int32
	for _, cs := range pod.Status.InitContainerStatuses {
		gen += cs.RestartCount
	}
	for _, cs := range pod.Status.ContainerStatuses {
		gen += cs.RestartCount
	}
	return gen
}
