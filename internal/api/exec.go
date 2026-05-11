package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
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
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated,omitempty"`
}

const maxExecOutputBytes = 10 << 20 // 10 MiB per stream

var (
	errExecPodLookup     = errors.New("list app pods")
	errExecPodNotRunning = errors.New("no running app pod")
)

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	remaining := lb.limit - lb.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		lb.buf.Write(p[:remaining])
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string { return lb.buf.String() }
func (lb *limitedBuffer) Len() int       { return lb.buf.Len() }

// @Summary Execute a command in an app pod
// @Description Runs a command in a running pod of the specified app and targets the app container explicitly, returning stdout/stderr
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
	projectName := chi.URLParam(r, "project")
	if !s.authorize(w, r, authz.Resource{Kind: "app", Project: projectName, Environment: envFromQuery(r)}, authz.ActionUpdate) {
		return
	}

	app, env, ok := s.resolveExecTarget(w, r)
	if !ok {
		return
	}
	if _, ok := constants.ProjectFromControlNs(app.Namespace); !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{fmt.Sprintf("app %q not in a control namespace (%q)", app.Name, app.Namespace)})
		return
	}
	envNs, err := envNamespace(app, env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

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
		slog.Error("exec: server has no rest.Config; exec is unavailable", "namespace", envNs, "app", app.Name)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"exec is not available on this server"})
		return
	}

	// Find a running pod for this app in the env namespace and target the app container explicitly.
	podName, containerName, err := s.findAppPod(r.Context(), envNs, app.Name, env)
	if err != nil {
		slog.Error("exec: failed to find app pod", "namespace", envNs, "app", app.Name, "err", err)
		if errors.Is(err, errExecPodNotRunning) {
			writeJSON(w, http.StatusNotFound, errorResponse{fmt.Sprintf("no running pod found for app %q", app.Name)})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to locate app pod"})
		return
	}

	stdout, stderr, truncated, err := s.execInPod(r.Context(), envNs, podName, containerName, req.Command)
	if err != nil {
		slog.Error("exec: streaming failed", "namespace", envNs, "app", app.Name, "pod", podName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"exec failed"})
		return
	}

	writeJSON(w, http.StatusOK, execResponse{Stdout: stdout, Stderr: stderr, Truncated: truncated})
}

// findAppPod returns the first running pod and app container matching the app label.
func (s *Server) findAppPod(ctx context.Context, ns, appName, env string) (string, string, error) {
	pods, err := s.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s,app.kubernetes.io/managed-by=mortise,%s=%s",
			constants.AppNameLabel, appName, constants.EnvironmentLabel, env),
	})
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", errExecPodLookup, err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		containerName, ok := firstAppContainerName(pod, appName)
		if !ok {
			continue
		}
		return pod.Name, containerName, nil
	}
	return "", "", fmt.Errorf("%w for app %q", errExecPodNotRunning, appName)
}

func firstAppContainerName(pod *corev1.Pod, appName string) (string, bool) {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == appName {
			return pod.Spec.Containers[i].Name, true
		}
	}
	if len(pod.Spec.Containers) == 1 {
		return pod.Spec.Containers[0].Name, true
	}
	return "", false
}

// execInPod runs a command in the selected container of the named pod.
func (s *Server) execInPod(ctx context.Context, ns, podName, containerName string, command []string) (string, string, bool, error) {
	req := s.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   command,
			Container: containerName,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(s.restConfig, "POST", req.URL())
	if err != nil {
		return "", "", false, fmt.Errorf("creating executor: %w", err)
	}

	stdout := &limitedBuffer{limit: maxExecOutputBytes}
	stderr := &limitedBuffer{limit: maxExecOutputBytes}
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
	}); err != nil {
		return stdout.String(), stderr.String(), false, err
	}

	truncated := stdout.Len() >= maxExecOutputBytes || stderr.Len() >= maxExecOutputBytes
	return stdout.String(), stderr.String(), truncated, nil
}

func (s *Server) resolveExecTarget(w http.ResponseWriter, r *http.Request) (*mortisev1alpha1.App, string, bool) {
	project, ok := s.getProject(w, r)
	if !ok {
		return nil, "", false
	}
	appName := chi.URLParam(r, "app")
	env := envFromQuery(r)
	if indexOfEnv(project, env) < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf(
			"environment %q is not declared on project %q — add it via POST /api/projects/%s/environments first",
			env, project.Name, project.Name)})
		return nil, "", false
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: projectNs(project)}, &app); err != nil {
		writeError(w, r, err)
		return nil, "", false
	}
	return &app, env, true
}
