package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

type logLine struct {
	Line string `json:"line"`
	Pod  string `json:"pod"`
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	env := r.URL.Query().Get("env")
	if env == "" {
		env = "production"
	}
	follow := r.URL.Query().Get("follow") == "true"

	// Resolve the App CRD.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	// Find pods matching the Deployment's label selector.
	sel := labels.SelectorFromSet(map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	})
	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	if len(podList.Items) == 0 {
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("no pods found for app %q env %q", name, env)})
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"streaming not supported"})
		return
	}

	var tailLines int64 = 100
	logOpts := &corev1.PodLogOptions{
		Follow:    follow,
		TailLines: &tailLines,
	}

	ctx := r.Context()

	for _, pod := range podList.Items {
		podName := pod.Name
		stream, err := s.clientset.CoreV1().Pods(ns).GetLogs(podName, logOpts).Stream(ctx)
		if err != nil {
			// Write error as SSE event and continue to next pod.
			data, _ := json.Marshal(logLine{Line: fmt.Sprintf("error streaming logs: %v", err), Pod: podName})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			continue
		}
		defer stream.Close()

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			data, _ := json.Marshal(logLine{Line: scanner.Text(), Pod: podName})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
