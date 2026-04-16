package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

type logLine struct {
	Pod    string `json:"pod"`
	Line   string `json:"line"`
	Stream string `json:"stream"`
}

// handleLogs streams pod logs for an App environment via Server-Sent Events.
// All pods matching the Deployment's label selector are aggregated onto the
// single response; each line is tagged with its pod name. New pods created
// during the stream (e.g. rollouts) are picked up via a pod watcher and their
// logs are joined into the stream.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "app")

	env := r.URL.Query().Get("env")
	if env == "" {
		env = "production"
	}
	follow := r.URL.Query().Get("follow") == "true"

	tailLines := int64(100)
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil && n >= 0 {
			tailLines = n
		}
	}

	// Resolve the App CRD (404 if missing).
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	selSet := map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	}
	sel := labels.SelectorFromSet(selSet)

	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: sel}); err != nil {
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

	streamPod := func(podName string) {
		if !started.add(podName) {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.streamPodLogs(ctx, writer, ns, podName, tailLines, follow)
		}()
	}

	for _, p := range podList.Items {
		streamPod(p.Name)
	}

	// If follow=true, watch for new pods that match the selector (e.g. rollouts)
	// and start streaming them too.
	if follow {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchPods(ctx, ns, sel.String(), streamPod)
		}()
	}

	wg.Wait()
}

// streamPodLogs streams logs from a single pod onto the shared SSE writer.
// Exits cleanly when the pod terminates, the context is cancelled, or the
// log stream returns EOF.
func (s *Server) streamPodLogs(ctx context.Context, w *sseWriter, ns, podName string, tailLines int64, follow bool) {
	opts := &corev1.PodLogOptions{
		Follow:    follow,
		TailLines: &tailLines,
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
		w.writeEvent(logLine{Pod: podName, Line: scanner.Text(), Stream: "stdout"})
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil && err != io.EOF {
		w.writeEvent(logLine{Pod: podName, Line: fmt.Sprintf("stream ended: %v", err), Stream: "stderr"})
	}
}

// watchPods uses the clientset to watch for new pods matching the label
// selector and invokes onAdd whenever a pod is added or modified into a
// state worth streaming. Returns when ctx is cancelled.
func (s *Server) watchPods(ctx context.Context, ns, labelSelector string, onAdd func(string)) {
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
			onAdd(pod.Name)
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

// podTracker records which pod names have already had a streaming goroutine
// started so a pod watcher re-reporting an existing pod doesn't spawn
// duplicate log streams.
type podTracker struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newPodTracker() *podTracker {
	return &podTracker{seen: map[string]struct{}{}}
}

func (p *podTracker) add(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.seen[name]; ok {
		return false
	}
	p.seen[name] = struct{}{}
	return true
}
