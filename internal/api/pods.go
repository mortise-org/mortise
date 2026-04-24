package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// podSummary is the per-pod shape returned by GET /pods. It's the minimum the
// UI's pod picker needs — name, phase, restarts, ready, and two timestamps —
// so we can render diagnosis chips without pulling the full PodSpec over.
type podSummary struct {
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	RestartCount int32  `json:"restartCount"`
	Ready        bool   `json:"ready"`
	StartedAt    string `json:"startedAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// handleListPods returns summaries of the pods backing an App environment.
// Returns [] (200) when no pods match so the UI doesn't spam errors between
// rollouts. 404 is reserved for missing Project / App.
//
// GET /api/projects/{project}/apps/{app}/pods?env={env}
//
// @Summary List pods for an app
// @Description Returns pod summaries (name, phase, restarts, ready state, timestamps) for the app environment
// @Tags pods
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param env query string false "Environment name (default: production)"
// @Success 200 {array} podSummary
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /projects/{project}/apps/{app}/pods [get]
func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
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

	envNs := constants.EnvNamespace(projectName, env)

	sel := labels.SelectorFromSet(map[string]string{
		constants.AppNameLabel:         name,
		"app.kubernetes.io/managed-by": "mortise",
		"mortise.dev/environment":      env,
	})

	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList, client.InNamespace(envNs), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	out := make([]podSummary, 0, len(podList.Items))
	for i := range podList.Items {
		out = append(out, summarizePod(&podList.Items[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// summarizePod extracts the fields podSummary exposes. Kept separate so it's
// unit-testable without constructing an HTTP request.
func summarizePod(pod *corev1.Pod) podSummary {
	var restarts int32
	for _, cs := range pod.Status.ContainerStatuses {
		restarts += cs.RestartCount
	}

	ready := false
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			ready = true
			break
		}
	}

	// Earliest Running.StartedAt across the pod's containers. Containers that
	// aren't running contribute nothing.
	var earliest *time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running == nil {
			continue
		}
		t := cs.State.Running.StartedAt.Time
		if t.IsZero() {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = &t
		}
	}

	started := ""
	if earliest != nil {
		started = earliest.UTC().Format(time.RFC3339)
	}

	return podSummary{
		Name:         pod.Name,
		Phase:        string(pod.Status.Phase),
		RestartCount: restarts,
		Ready:        ready,
		StartedAt:    started,
		CreatedAt:    pod.CreationTimestamp.UTC().Format(time.RFC3339),
	}
}
