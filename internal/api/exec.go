package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

type execRequest struct {
	Command []string `json:"command"`
}

type execResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// @Summary Execute a command in an app pod
// @Description Runs a command in the first running pod of the specified app and returns stdout/stderr
// @Tags exec
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param project path string true "Project name"
// @Param app path string true "App name"
// @Param env query string false "Environment name (default: production)"
// @Param body body execRequest true "Command to execute"
// @Success 200 {object} execResponse
// @Failure 400 {object} errorResponse
// @Failure 403 {object} errorResponse
// @Failure 404 {object} errorResponse
// @Failure 500 {object} errorResponse
// @Router /projects/{project}/apps/{app}/exec [post]
func (s *Server) ExecInApp(w http.ResponseWriter, r *http.Request) {
	_, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	env := envFromQuery(r)
	envNs := constants.EnvNamespace(projectName, env)

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if len(req.Command) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{"command is required"})
		return
	}

	if s.restConfig == nil {
		slog.Error("exec: server has no rest.Config; exec is unavailable", "namespace", envNs, "app", appName)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"exec is not available on this server"})
		return
	}

	// Find the first running pod for this app in the env namespace.
	podName, err := s.findAppPod(r.Context(), envNs, appName)
	if err != nil {
		slog.Error("exec: failed to find app pod", "namespace", envNs, "app", appName, "err", err)
		writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("no running pod found for app %q", appName)})
		return
	}

	stdout, stderr, err := s.execInPod(r.Context(), envNs, podName, req.Command)
	if err != nil {
		slog.Error("exec: streaming failed", "namespace", envNs, "app", appName, "pod", podName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"exec failed"})
		return
	}

	writeJSON(w, http.StatusOK, execResponse{Stdout: stdout, Stderr: stderr})
}

// findAppPod returns the name of the first running pod matching the app label.
func (s *Server) findAppPod(ctx context.Context, ns, appName string) (string, error) {
	pods, err := s.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", appName),
		Limit:         1,
	})
	if err != nil {
		return "", fmt.Errorf("listing pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for app %q", appName)
	}
	return pods.Items[0].Name, nil
}

// execInPod runs a command in the first container of the named pod.
func (s *Server) execInPod(ctx context.Context, ns, podName string, command []string) (string, string, error) {
	req := s.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(s.restConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("creating executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return stdout.String(), stderr.String(), err
	}

	return stdout.String(), stderr.String(), nil
}
