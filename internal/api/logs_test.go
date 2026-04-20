package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/api"
	"github.com/MC-Meesh/mortise/internal/auth"
)

// newLogsServer returns a Server wired with the supplied fake clientset and a
// bearer token, so tests can both seed pod objects and inspect which
// PodLogOptions the handler forwarded to GetLogs.
func newLogsServer(t *testing.T, k8sClient client.Client, cs *fake.Clientset) (*api.Server, string) {
	t.Helper()
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "logs@example.com", "testpass", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "logs@example.com", Password: "testpass"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	testToken = token

	srv := api.NewServer(k8sClient, cs, nil, authProvider, jwtHelper, nil)
	return srv, token
}

// lastPodLogOptions returns the PodLogOptions passed to the most recent
// GetLogs call on the fake clientset, or nil if none were recorded.
func lastPodLogOptions(cs *fake.Clientset) *corev1.PodLogOptions {
	for i := len(cs.Actions()) - 1; i >= 0; i-- {
		a := cs.Actions()[i]
		if a.GetVerb() != "get" || a.GetSubresource() != "log" {
			continue
		}
		ga, ok := a.(clienttesting.GenericAction)
		if !ok {
			continue
		}
		opts, ok := ga.GetValue().(*corev1.PodLogOptions)
		if !ok {
			continue
		}
		return opts
	}
	return nil
}

// seedAppAndPod creates an App CRD and a matching Pod in envtest so the logs
// handler sees a pod to stream.
func seedAppAndPod(t *testing.T, k8sClient client.Client, ns, appName, env, podName string) {
	t.Helper()
	ctx := context.Background()

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(ctx, app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":       appName,
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      env,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(ctx, pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}
}

func TestLogsNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/no-such-app/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestLogsNonexistentProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/anything/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for logs in nonexistent project, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogsNoPods(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "logs-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/logs-app/logs", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for no pods, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message about no pods")
	}
}

func TestLogsErrorResponseIsJSON(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "headers-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/headers-app/logs", nil)
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json for error response, got %s", ct)
	}
}

// TestLogsAcceptsTailAndEnvParams exercises the tail + env query parsing
// without needing real pod streaming. Uses a nonexistent env so the handler
// 404s after the parsing step.
func TestLogsAcceptsTailAndEnvParams(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "params-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/params-app/logs?tail=50&env=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for no pods, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message")
	}
}

// TestLogsSSEHeadersWithPod verifies SSE headers and event structure when a
// matching Pod object exists. The fake clientset returns synthetic log output
// regardless of the pod's real runtime state.
func TestLogsSSEHeadersWithPod(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	nsName := seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "stream-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stream-app-pod-1",
			Namespace: nsName,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "stream-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "production",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(context.Background(), pod); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/stream-app/logs?env=production&tail=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %s", cc)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("expected SSE body to start with 'data: ', got: %q", body)
	}

	line := strings.TrimPrefix(body, "data: ")
	if idx := strings.Index(line, "\n"); idx > 0 {
		line = line[:idx]
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("parse SSE event JSON: %v (line=%q)", err, line)
	}
	if ev["pod"] != "stream-app-pod-1" {
		t.Errorf("expected pod field %q, got %v", "stream-app-pod-1", ev["pod"])
	}
	if _, ok := ev["line"]; !ok {
		t.Errorf("expected line field in SSE event, got %v", ev)
	}
	if _, ok := ev["stream"]; !ok {
		t.Errorf("expected stream field in SSE event, got %v", ev)
	}
}

// TestLogsAcceptsTokenQueryParam verifies that the /logs endpoint treats
// ?token=<jwt> as a fallback when the Authorization header is absent
// (EventSource workaround).
func TestLogsAcceptsTokenQueryParam(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv, token := newTestServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "tok-app",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	path := "/api/projects/default/apps/tok-app/logs?token=" + token
	w := doRequestWithToken(h, http.MethodGet, path, nil, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (auth passed, no pods), got %d: %s", w.Code, w.Body.String())
	}
}

// TestLogsRequiresAuth verifies that /logs still requires authentication —
// the query-param token fallback doesn't weaken the auth requirement.
func TestLogsRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/anything/logs", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestHandleLogsPassesPreviousFlag(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "prev-app", "production", "prev-app-pod-1")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/prev-app/logs?previous=true", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	opts := lastPodLogOptions(cs)
	if opts == nil {
		t.Fatal("expected GetLogs to be invoked")
	}
	if !opts.Previous {
		t.Errorf("expected PodLogOptions.Previous=true, got %v", opts.Previous)
	}
}

func TestHandleLogsPassesSinceSeconds(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "secs-app", "production", "secs-app-pod-1")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/secs-app/logs?sinceSeconds=300", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	opts := lastPodLogOptions(cs)
	if opts == nil {
		t.Fatal("expected GetLogs to be invoked")
	}
	if opts.SinceSeconds == nil || *opts.SinceSeconds != 300 {
		t.Errorf("expected PodLogOptions.SinceSeconds=300, got %v", opts.SinceSeconds)
	}
}

func TestHandleLogsPassesSinceTime(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "time-app", "production", "time-app-pod-1")

	when := "2025-01-02T03:04:05Z"
	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/time-app/logs?sinceTime="+when, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	opts := lastPodLogOptions(cs)
	if opts == nil {
		t.Fatal("expected GetLogs to be invoked")
	}
	if opts.SinceTime == nil {
		t.Fatalf("expected PodLogOptions.SinceTime to be set")
	}
	want, _ := time.Parse(time.RFC3339, when)
	if !opts.SinceTime.Time.Equal(want) {
		t.Errorf("expected SinceTime=%s, got %s", when, opts.SinceTime.Time.Format(time.RFC3339))
	}
}

func TestHandleLogsPreferSinceTimeOverSeconds(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "both-app", "production", "both-app-pod-1")

	path := "/api/projects/default/apps/both-app/logs?sinceSeconds=300&sinceTime=2025-01-02T03:04:05Z"
	w := doRequest(h, http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	opts := lastPodLogOptions(cs)
	if opts == nil {
		t.Fatal("expected GetLogs to be invoked")
	}
	if opts.SinceTime == nil {
		t.Error("expected SinceTime to be set when both params provided")
	}
	if opts.SinceSeconds != nil {
		t.Errorf("expected SinceSeconds=nil when SinceTime is set, got %v", *opts.SinceSeconds)
	}
}

func TestHandleLogsAlwaysSetsTimestamps(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "ts-app", "production", "ts-app-pod-1")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ts-app/logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	opts := lastPodLogOptions(cs)
	if opts == nil {
		t.Fatal("expected GetLogs to be invoked")
	}
	if !opts.Timestamps {
		t.Error("expected PodLogOptions.Timestamps=true")
	}
}

// TestLogLineTimestampParsed exercises the line-prefix parser directly. The
// fake clientset's log stream is hardcoded to "fake logs", so we can't round-
// trip an arbitrary input through the HTTP path — the parser is the unit worth
// testing.
func TestLogLineTimestampParsed(t *testing.T) {
	ts, line := api.ParseLogLineForTest("2025-01-01T14:32:01.123456789Z hello world")
	if ts != "2025-01-01T14:32:01.123456789Z" {
		t.Errorf("expected ts=2025-01-01T14:32:01.123456789Z, got %q", ts)
	}
	if line != "hello world" {
		t.Errorf("expected line=%q, got %q", "hello world", line)
	}
}

func TestLogLineTimestampMissing(t *testing.T) {
	ts, line := api.ParseLogLineForTest("hello world")
	if ts != "" {
		t.Errorf("expected empty ts, got %q", ts)
	}
	if line != "hello world" {
		t.Errorf("expected line=%q, got %q", "hello world", line)
	}
}

func TestHandleLogsPodParamFiltersToSinglePod(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "pin-app", "production", "pin-app-pod-1")

	// Seed an extra matching pod to prove the handler ignored the selector
	// list when ?pod= is set (otherwise we'd see 2 GetLogs calls).
	extra := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pin-app-pod-2",
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "pin-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "production",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(context.Background(), extra); err != nil {
		t.Fatalf("create extra pod: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/pin-app/logs?pod=pin-app-pod-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var getLogs int
	for _, a := range cs.Actions() {
		if a.GetVerb() == "get" && a.GetSubresource() == "log" {
			getLogs++
		}
	}
	if getLogs != 1 {
		t.Errorf("expected exactly 1 GetLogs call for pinned pod, got %d", getLogs)
	}
}

// TestHandleLogsPodParamWrongLabels rejects pinning to a pod in the same
// namespace that doesn't carry the app/env labels — stops a user from
// streaming an arbitrary pod by name.
func TestHandleLogsPodParamWrongLabels(t *testing.T) {
	k8sClient := setupEnvtest(t)
	cs := fake.NewClientset()
	srv, _ := newLogsServer(t, k8sClient, cs)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")
	seedAppAndPod(t, k8sClient, ns, "guard-app", "production", "guard-app-pod-1")

	// Foreign pod in the same namespace, wrong labels.
	foreign := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-pod",
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name": "some-other-app",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25.0"}},
		},
	}
	if err := k8sClient.Create(context.Background(), foreign); err != nil {
		t.Fatalf("create foreign pod: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/guard-app/logs?pod=other-pod", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for foreign pod, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleBuildLogsFallsBackToConfigMap verifies that when no in-memory
// tracker exists for the app (operator restarted, or the build finished
// before this request), the handler loads the persisted `buildlogs-{app}`
// ConfigMap and returns its contents plus the status/commitSHA/timestamp
// annotations.
func TestHandleBuildLogsFallsBackToConfigMap(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	nsName := seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "cm-fallback",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildlogs-cm-fallback",
			Namespace: nsName,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-04-20T14:00:00Z",
				"mortise.dev/build-commit":    "abc123",
				"mortise.dev/build-status":    "Succeeded",
			},
		},
		Data: map[string]string{
			"lines": "step 1/3: FROM alpine\nstep 2/3: COPY . /app\nstep 3/3: CMD [\"/app/bin\"]",
		},
	}
	if err := k8sClient.Create(context.Background(), cm); err != nil {
		t.Fatalf("plant configmap: %v", err)
	}

	// No in-memory tracker wired → fallback path reads the ConfigMap.
	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/cm-fallback/build-logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["building"] != false {
		t.Errorf("expected building=false, got %v", resp["building"])
	}
	if resp["status"] != "Succeeded" {
		t.Errorf("expected status=Succeeded, got %v", resp["status"])
	}
	if resp["commitSHA"] != "abc123" {
		t.Errorf("expected commitSHA=abc123, got %v", resp["commitSHA"])
	}
	if resp["timestamp"] != "2026-04-20T14:00:00Z" {
		t.Errorf("expected timestamp=2026-04-20T14:00:00Z, got %v", resp["timestamp"])
	}
	if resp["error"] != "" {
		t.Errorf("expected empty error on Succeeded, got %v", resp["error"])
	}

	linesAny, ok := resp["lines"].([]any)
	if !ok {
		t.Fatalf("expected lines to be array, got %T: %v", resp["lines"], resp["lines"])
	}
	if len(linesAny) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(linesAny), linesAny)
	}
	if !strings.Contains(linesAny[0].(string), "step 1/3") {
		t.Errorf("expected first line to contain 'step 1/3', got %q", linesAny[0])
	}
}

// TestHandleBuildLogsFallbackSurfacesFailedStatus verifies the Failed path
// reports the build-error annotation in the `error` response field.
func TestHandleBuildLogsFallbackSurfacesFailedStatus(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	nsName := seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "cm-fallback-fail",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "buildlogs-cm-fallback-fail",
			Namespace: nsName,
			Annotations: map[string]string{
				"mortise.dev/build-timestamp": "2026-04-20T14:05:00Z",
				"mortise.dev/build-commit":    "deadbeef",
				"mortise.dev/build-status":    "Failed",
				"mortise.dev/build-error":     "dockerfile not found",
			},
		},
		Data: map[string]string{"lines": "some log output"},
	}
	if err := k8sClient.Create(context.Background(), cm); err != nil {
		t.Fatalf("plant configmap: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/cm-fallback-fail/build-logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if resp["status"] != "Failed" {
		t.Errorf("expected status=Failed, got %v", resp["status"])
	}
	if resp["error"] != "dockerfile not found" {
		t.Errorf("expected error='dockerfile not found', got %v", resp["error"])
	}
}

// TestHandleBuildLogsNoTrackerNoConfigMap verifies the zero case: returns
// empty lines and building=false when neither an in-memory tracker nor a
// persisted ConfigMap exists.
func TestHandleBuildLogsNoTrackerNoConfigMap(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	doRequest(h, http.MethodPost, "/api/projects/default/apps", map[string]any{
		"name": "cm-fallback-empty",
		"spec": map[string]any{
			"source": map[string]any{"type": "image", "image": "nginx:1.25.0"},
		},
	})

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/cm-fallback-empty/build-logs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if resp["building"] != false {
		t.Errorf("expected building=false, got %v", resp["building"])
	}
	linesAny, ok := resp["lines"].([]any)
	if !ok {
		t.Fatalf("expected lines array, got %T", resp["lines"])
	}
	if len(linesAny) != 0 {
		t.Errorf("expected empty lines, got %v", linesAny)
	}
}
