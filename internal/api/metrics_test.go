package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/api"
	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// newMetricsServer builds a Server with a fake metrics client wired up.
func newMetricsServer(t *testing.T, k8sClient client.Client, mc *metricsfake.Clientset) *api.Server {
	t.Helper()
	ctx := context.Background()

	_ = k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "mortise-system"},
	})

	authProvider := auth.NewNativeAuthProvider(k8sClient)
	jwtHelper := auth.NewJWTHelper(k8sClient)
	if err := authProvider.CreateUser(ctx, "metrics@example.com", "testpass", auth.RoleAdmin); err != nil {
		t.Fatalf("create user: %v", err)
	}
	principal, err := authProvider.Authenticate(ctx, auth.Credentials{Email: "metrics@example.com", Password: "testpass"})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	token, err := jwtHelper.GenerateToken(ctx, principal)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	testToken = token

	cs := fake.NewClientset()
	srv := api.NewServer(k8sClient, cs, nil, nil, authProvider, jwtHelper, nil, authz.NewNativePolicyEngine(k8sClient))
	srv.SetMetricsClient(mc.MetricsV1beta1())
	return srv
}

func fakeMetricsClient(items ...metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	mc := metricsfake.NewSimpleClientset()
	mc.Fake.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		la := action.(clienttesting.ListAction)
		ns := la.GetNamespace()
		filtered := make([]metricsv1beta1.PodMetrics, 0, len(items))
		for _, pm := range items {
			if ns == "" || pm.Namespace == ns {
				filtered = append(filtered, pm)
			}
		}
		return true, &metricsv1beta1.PodMetricsList{Items: filtered}, nil
	})
	return mc
}

// --- handleMetricsCurrent ---

func TestMetricsCurrentReturnsAvailableFalseWithoutMetricsClient(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mc-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mc-app/metrics/current?env=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != false {
		t.Errorf("expected available=false when no metrics client, got %v", resp["available"])
	}
}

func TestMetricsCurrentReturnsPodsWithMetrics(t *testing.T) {
	k8sClient := setupEnvtest(t)
	mc := fakeMetricsClient(metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mc-app-pod-1",
			Namespace: constants.EnvNamespace("default", "production"),
			Labels: map[string]string{
				"app.kubernetes.io/name":       "mc-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "production",
			},
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "app",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		},
	})

	srv := newMetricsServer(t, k8sClient, mc)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mc-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mc-app/metrics/current?env=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != true {
		t.Errorf("expected available=true, got %v", resp["available"])
	}
	pods, ok := resp["pods"].([]any)
	if !ok {
		t.Fatalf("expected pods array, got %T", resp["pods"])
	}
	if len(pods) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(pods))
	}
	pod := pods[0].(map[string]any)
	if pod["name"] != "mc-app-pod-1" {
		t.Errorf("expected pod name mc-app-pod-1, got %v", pod["name"])
	}
	if pod["cpu"] != 0.5 {
		t.Errorf("expected cpu=0.5, got %v", pod["cpu"])
	}
	if pod["memory"] != float64(256*1024*1024) {
		t.Errorf("expected memory=%d, got %v", 256*1024*1024, pod["memory"])
	}
}

func TestMetricsCurrentPrefersAdapterWhenConfigured(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pods":[{"name":"pod-a","cpu":[[1700000000,0.2],[1700000015,0.35]],"memory":[[1700000000,12345],[1700000015,45678]]}]}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	mc := fakeMetricsClient(metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mc-app-pod-1",
			Namespace: constants.EnvNamespace("default", "production"),
			Labels: map[string]string{
				"app.kubernetes.io/name":       "mc-app",
				"app.kubernetes.io/managed-by": "mortise",
				"mortise.dev/environment":      "production",
			},
		},
		Containers: []metricsv1beta1.ContainerMetrics{{
			Name: "app",
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("900m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		}},
	})
	srv := newMetricsServer(t, k8sClient, mc)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mc-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}
	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Observability: mortisev1alpha1.ObservabilitySpec{MetricsAdapterEndpoint: adapter.URL},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mc-app/metrics/current?env=production", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != true {
		t.Fatalf("expected available=true, got %v", resp["available"])
	}
	pods, ok := resp["pods"].([]any)
	if !ok || len(pods) != 1 {
		t.Fatalf("expected one pod from adapter response, got %#v", resp["pods"])
	}
	pod := pods[0].(map[string]any)
	if pod["name"] != "pod-a" {
		t.Errorf("pod name = %v, want pod-a", pod["name"])
	}
	if pod["cpu"] != 0.35 {
		t.Errorf("cpu = %v, want 0.35", pod["cpu"])
	}
	if pod["memory"] != float64(45678) {
		t.Errorf("memory = %v, want 45678", pod["memory"])
	}
}

func TestMetricsCurrentNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/no-such/metrics/current?env=production", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetricsCurrentNonexistentProject(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequest(h, http.MethodGet, "/api/projects/ghost/apps/x/metrics/current", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetricsCurrentRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/x/metrics/current", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// --- handleMetricsHistory ---

func TestMetricsHistoryMissingStartEnd(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mh-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mh-app/metrics?env=production", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without start/end, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetricsHistoryAvailableFalseWithoutPlatformConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mh-nopc", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mh-nopc/metrics?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != false {
		t.Errorf("expected available=false without PlatformConfig, got %v", resp["available"])
	}
}

func TestMetricsHistoryAvailableFalseWithEmptyEndpoint(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mh-noep", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mh-noep/metrics?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != false {
		t.Errorf("expected available=false with empty endpoint, got %v", resp["available"])
	}
}

func TestMetricsHistoryProxiesToAdapter(t *testing.T) {
	var gotQuery map[string]string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{
			"namespace": r.URL.Query().Get("namespace"),
			"app":       r.URL.Query().Get("app"),
			"env":       r.URL.Query().Get("env"),
			"start":     r.URL.Query().Get("start"),
			"end":       r.URL.Query().Get("end"),
			"step":      r.URL.Query().Get("step"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pods":[]}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mh-proxy", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
			Observability: mortisev1alpha1.ObservabilitySpec{
				MetricsAdapterEndpoint: adapter.URL,
			},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/mh-proxy/metrics?env=production&start=1700000000&end=1700003600&step=120", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if gotQuery["namespace"] != constants.EnvNamespace("default", "production") {
		t.Errorf("namespace = %q, want %q", gotQuery["namespace"], constants.EnvNamespace("default", "production"))
	}
	if gotQuery["app"] != "mh-proxy" {
		t.Errorf("app = %q, want mh-proxy", gotQuery["app"])
	}
	if gotQuery["env"] != "production" {
		t.Errorf("env = %q, want production", gotQuery["env"])
	}
	if gotQuery["start"] != "1700000000" {
		t.Errorf("start = %q, want 1700000000", gotQuery["start"])
	}
	if gotQuery["end"] != "1700003600" {
		t.Errorf("end = %q, want 1700003600", gotQuery["end"])
	}
	if gotQuery["step"] != "120" {
		t.Errorf("step = %q, want 120", gotQuery["step"])
	}
}

func TestMetricsHistoryDefaultStep(t *testing.T) {
	var gotStep string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStep = r.URL.Query().Get("step")
		w.Write([]byte(`{"pods":[]}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "mh-step", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Observability: mortisev1alpha1.ObservabilitySpec{
				MetricsAdapterEndpoint: adapter.URL,
			},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	doRequest(h, http.MethodGet, "/api/projects/default/apps/mh-step/metrics?env=production&start=1700000000&end=1700003600", nil)

	if gotStep != "60" {
		t.Errorf("default step = %q, want 60", gotStep)
	}
}

func TestMetricsHistoryNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/metrics?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMetricsHistoryRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/x/metrics?start=0&end=1", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// --- handleLogHistory ---

func TestLogHistoryMissingStartEnd(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-app", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-app/logs/history?env=production", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without start/end, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogHistoryAvailableFalseWithoutPlatformConfig(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-nopc", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-nopc/logs/history?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != false {
		t.Errorf("expected available=false without PlatformConfig, got %v", resp["available"])
	}
}

func TestLogHistoryAvailableFalseWithEmptyEndpoint(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-noep", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec:       mortisev1alpha1.PlatformConfigSpec{Domain: "example.com"},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-noep/logs/history?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["available"] != false {
		t.Errorf("expected available=false with empty logs endpoint, got %v", resp["available"])
	}
}

func TestLogHistoryProxiesToAdapter(t *testing.T) {
	var gotQuery map[string]string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = map[string]string{
			"namespace": r.URL.Query().Get("namespace"),
			"app":       r.URL.Query().Get("app"),
			"env":       r.URL.Query().Get("env"),
			"start":     r.URL.Query().Get("start"),
			"end":       r.URL.Query().Get("end"),
			"limit":     r.URL.Query().Get("limit"),
			"filter":    r.URL.Query().Get("filter"),
			"before":    r.URL.Query().Get("before"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"lines":[],"hasMore":false}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-proxy", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Domain: "example.com",
			Observability: mortisev1alpha1.ObservabilitySpec{
				LogsAdapterEndpoint: adapter.URL,
			},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-proxy/logs/history?env=production&start=1700000000&end=1700003600&limit=100&filter=error&before=2025-01-01T00:00:00Z", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if gotQuery["namespace"] != constants.EnvNamespace("default", "production") {
		t.Errorf("namespace = %q, want %q", gotQuery["namespace"], constants.EnvNamespace("default", "production"))
	}
	if gotQuery["app"] != "lh-proxy" {
		t.Errorf("app = %q, want lh-proxy", gotQuery["app"])
	}
	if gotQuery["env"] != "production" {
		t.Errorf("env = %q, want production", gotQuery["env"])
	}
	if gotQuery["start"] != "1700000000" {
		t.Errorf("start = %q, want 1700000000", gotQuery["start"])
	}
	if gotQuery["end"] != "1700003600" {
		t.Errorf("end = %q, want 1700003600", gotQuery["end"])
	}
	if gotQuery["limit"] != "100" {
		t.Errorf("limit = %q, want 100", gotQuery["limit"])
	}
	if gotQuery["filter"] != "error" {
		t.Errorf("filter = %q, want error", gotQuery["filter"])
	}
	if gotQuery["before"] != "2025-01-01T00:00:00Z" {
		t.Errorf("before = %q, want 2025-01-01T00:00:00Z", gotQuery["before"])
	}
}

func TestLogHistoryDefaultLimit(t *testing.T) {
	var gotLimit string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Write([]byte(`{"lines":[]}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-lim", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Observability: mortisev1alpha1.ObservabilitySpec{
				LogsAdapterEndpoint: adapter.URL,
			},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-lim/logs/history?env=production&start=1700000000&end=1700003600", nil)

	if gotLimit != "500" {
		t.Errorf("default limit = %q, want 500", gotLimit)
	}
}

func TestLogHistoryLimitCap(t *testing.T) {
	var gotLimit string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Write([]byte(`{"lines":[]}`))
	}))
	defer adapter.Close()

	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	ns := seedProject(t, k8sClient, "default")

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "lh-cap", Namespace: ns},
		Spec: mortisev1alpha1.AppSpec{
			Source: mortisev1alpha1.AppSource{Type: mortisev1alpha1.SourceTypeImage, Image: "nginx:1.25.0"},
		},
	}
	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	pc := &mortisev1alpha1.PlatformConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "platform"},
		Spec: mortisev1alpha1.PlatformConfigSpec{
			Observability: mortisev1alpha1.ObservabilitySpec{
				LogsAdapterEndpoint: adapter.URL,
			},
		},
	}
	if err := k8sClient.Create(context.Background(), pc); err != nil {
		t.Fatalf("create PlatformConfig: %v", err)
	}

	doRequest(h, http.MethodGet, "/api/projects/default/apps/lh-cap/logs/history?env=production&start=1700000000&end=1700003600&limit=9999", nil)

	if gotLimit != "2000" {
		t.Errorf("capped limit = %q, want 2000", gotLimit)
	}
}

func TestLogHistoryNonexistentApp(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")

	w := doRequest(h, http.MethodGet, "/api/projects/default/apps/ghost/logs/history?env=production&start=1700000000&end=1700003600", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLogHistoryRequiresAuth(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()

	w := doRequestWithToken(h, http.MethodGet, "/api/projects/default/apps/x/logs/history?start=0&end=1", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
