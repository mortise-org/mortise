//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func observerURL(path string, params map[string]string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d%s", observerLocalPort, path)
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	return u
}

func observerGet(t *testing.T, path string, params map[string]string) map[string]any {
	t.Helper()
	resp, err := http.Get(observerURL(path, params))
	if err != nil {
		t.Fatalf("observer GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("observer read body: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("observer GET %s: status %d, body: %s", path, resp.StatusCode, body)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("observer unmarshal: %v (body: %s)", err, body)
	}
	return result
}

// observerTryGet is like observerGet but returns (nil, false) on transient
// errors (network, 5xx) instead of calling t.Fatalf. For use inside polling
// loops where retries are expected.
func observerTryGet(t *testing.T, path string, params map[string]string) (map[string]any, bool) {
	t.Helper()
	resp, err := http.Get(observerURL(path, params))
	if err != nil {
		t.Logf("observer GET %s: %v", path, err)
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("observer read body: %v", err)
		return nil, false
	}
	if resp.StatusCode >= 500 {
		t.Logf("observer GET %s: transient %d, body: %s", path, resp.StatusCode, body)
		return nil, false
	}
	if resp.StatusCode != 200 {
		t.Fatalf("observer GET %s: status %d, body: %s", path, resp.StatusCode, body)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Logf("observer unmarshal: %v (body: %s)", err, body)
		return nil, false
	}
	return result, true
}

// requireCollectorAlive fails fast if the named collector never records a
// tick — i.e. its goroutine is dead or stuck — instead of letting the
// downstream data-wait blow its full ceiling with an opaque timeout. Every
// collector ticks each cycle regardless of whether our app's data is present
// (empty cycles still Record), so a recent lastTick means "alive, maybe slow"
// and its absence means "broken." This separates slow from broken so the
// data-waits below can carry generous ceilings for loaded/CI runners without
// masking a genuinely dead collector (mo-90d).
//
// The thresholds must outlast the test-peak load, not just be "big": under
// full-suite parallelism the observer briefly starves — its collectors' flush
// goroutines and even the health endpoint can stall for a sustained window
// (the traffic collector is most exposed, since the traffic test drives
// continuous load through the same observer it queries). A collector ticks
// every ~5s at rest, so no tick within 60s is genuinely stuck rather than
// slow; the 120s observation window rides out a bounded load spike before
// concluding the collector is dead. A truly dead collector never ticks and
// still fails at 120s with a clear signal.
func requireCollectorAlive(t *testing.T, collector string) {
	t.Helper()
	helpers.RequireEventually(t, 120*time.Second, func() bool {
		res, ok := observerTryGet(t, "/v1/health/collectors", nil)
		if !ok {
			return false
		}
		cols, _ := res["collectors"].([]any)
		for _, c := range cols {
			m, _ := c.(map[string]any)
			if name, _ := m["collector"].(string); name != collector {
				continue
			}
			lastTick, _ := m["lastTick"].(float64)
			return int64(lastTick) >= time.Now().Add(-60*time.Second).Unix()
		}
		return false
	})
}

func TestObserverHealth(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", observerLocalPort))
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode healthz response: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", result["status"])
	}
}

func TestObserverHealthBadEndpoint(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/nonexistent", observerLocalPort))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		t.Fatal("expected non-200 for nonexistent endpoint")
	}
}

func TestObserverMetrics(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	projectName := "obs-metrics-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "metrics-app"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, envNs, app.Name, 1)

	start := fmt.Sprintf("%d", time.Now().Add(-1*time.Minute).Unix())

	// Fail fast if the metrics collector never ticks; otherwise give
	// metrics-server warmup + scrape headroom on loaded runners (mo-90d).
	requireCollectorAlive(t, "metrics")

	// Poll until the observer has collected at least one metrics data point.
	helpers.RequireEventually(t, 180*time.Second, func() bool {
		end := fmt.Sprintf("%d", time.Now().Unix())
		result, ok := observerTryGet(t, "/v1/metrics", map[string]string{
			"namespace": envNs,
			"app":       app.Name,
			"env":       envName,
			"start":     start,
			"end":       end,
			"step":      "5",
		})
		if !ok {
			return false
		}

		pods, ok := result["pods"].([]any)
		if !ok || len(pods) == 0 {
			return false
		}

		pod := pods[0].(map[string]any)
		mem, ok := pod["memory"].([]any)
		return ok && len(mem) > 0
	})

	// Final assertion with structured checks.
	end := fmt.Sprintf("%d", time.Now().Unix())
	result := observerGet(t, "/v1/metrics", map[string]string{
		"namespace": envNs,
		"app":       app.Name,
		"env":       envName,
		"start":     start,
		"end":       end,
		"step":      "5",
	})

	pods := result["pods"].([]any)
	if len(pods) == 0 {
		t.Fatal("expected at least one pod in metrics response")
	}

	pod := pods[0].(map[string]any)
	podName, _ := pod["name"].(string)
	if podName == "" {
		t.Fatal("pod name is empty")
	}

	mem := pod["memory"].([]any)
	if len(mem) == 0 {
		t.Fatal("expected at least one memory data point")
	}

	// Memory should be > 0 for a running nginx pod.
	point := mem[0].([]any)
	memVal := point[1].(float64)
	if memVal <= 0 {
		t.Errorf("expected memory > 0, got %f", memVal)
	}
}

func TestObserverMetricsMissingParams(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/metrics?namespace=foo", observerLocalPort))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for missing params, got %d", resp.StatusCode)
	}
}

func TestObserverMetricsEmptyRange(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	// Query a namespace that doesn't exist — should return empty, not error.
	result := observerGet(t, "/v1/metrics", map[string]string{
		"namespace": "pj-nonexistent-production",
		"app":       "nope",
		"env":       "production",
		"start":     "0",
		"end":       "1",
		"step":      "5",
	})

	pods, ok := result["pods"].([]any)
	if !ok {
		pods = nil
	}
	if len(pods) != 0 {
		t.Fatalf("expected empty pods array, got %d entries", len(pods))
	}
}

func TestObserverLogs(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	projectName := "obs-logs-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "logs-app"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, envNs, app.Name, 1)

	start := fmt.Sprintf("%d", time.Now().Add(-2*time.Minute).Unix())

	// Fail fast if the logs collector never ticks; otherwise give the
	// tail-to-store pipeline headroom on loaded runners (mo-90d).
	requireCollectorAlive(t, "logs")

	// Poll until the observer has collected at least one log line.
	helpers.RequireEventually(t, 180*time.Second, func() bool {
		end := fmt.Sprintf("%d", time.Now().Add(1*time.Minute).Unix())
		result, ok := observerTryGet(t, "/v1/logs", map[string]string{
			"namespace": envNs,
			"app":       app.Name,
			"env":       envName,
			"start":     start,
			"end":       end,
		})
		if !ok {
			return false
		}

		lines, ok := result["lines"].([]any)
		return ok && len(lines) > 0
	})

	end := fmt.Sprintf("%d", time.Now().Add(1*time.Minute).Unix())
	result := observerGet(t, "/v1/logs", map[string]string{
		"namespace": envNs,
		"app":       app.Name,
		"env":       envName,
		"start":     start,
		"end":       end,
	})

	lines := result["lines"].([]any)
	if len(lines) == 0 {
		t.Fatal("expected at least one log line")
	}

	line := lines[0].(map[string]any)
	if ts, _ := line["ts"].(string); ts == "" {
		t.Error("log line missing timestamp")
	}
	if pod, _ := line["pod"].(string); pod == "" {
		t.Error("log line missing pod name")
	}
	if text, _ := line["text"].(string); text == "" {
		t.Error("log line missing text")
	}
}

func TestObserverLogsMissingParams(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/logs?namespace=foo&app=bar&env=prod", observerLocalPort))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for missing start/end, got %d", resp.StatusCode)
	}
}

func TestObserverLogsEmptyRange(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	result := observerGet(t, "/v1/logs", map[string]string{
		"namespace": "pj-nonexistent-production",
		"app":       "nope",
		"env":       "production",
		"start":     "0",
		"end":       "1",
	})

	lines, ok := result["lines"].([]any)
	if !ok {
		lines = nil
	}
	if len(lines) != 0 {
		t.Fatalf("expected empty lines, got %d", len(lines))
	}
}

func TestObserverTraffic(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)
	skipIfNoIngressClass(t)

	projectName := "obs-traffic-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "traffic-app"
	app.Spec.Network.Public = true
	app.Spec.Environments[0].Domain = "traffic-app.test"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, envNs, app.Name, 1)
	helpers.AssertIngressExists(t, k8sClient, envNs, app.Name)

	// Port-forward to Traefik and send requests with the correct Host header.
	traefikPort := helpers.PortForward(t, "mortise-deps", "mortise-traefik", 80)

	start := fmt.Sprintf("%d", time.Now().Add(-1*time.Minute).Unix())

	// Send requests continuously in the background. Traefik needs time to
	// discover the new Ingress, so early requests may 404 (no ServiceName in
	// the access log → traffic collector ignores them). Keeping the stream
	// going ensures we generate routable traffic once the route is live.
	stopTraffic := make(chan struct{})
	t.Cleanup(func() { close(stopTraffic) })
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		for {
			select {
			case <-stopTraffic:
				return
			default:
			}
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", traefikPort), nil)
			req.Host = "traffic-app.test"
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	// Fail fast if the traffic collector never ticks (dead); otherwise give
	// the end-to-end pipeline (traefik route discovery → access log → collect)
	// headroom on loaded runners (mo-90d).
	requireCollectorAlive(t, "traffic")

	// Poll until the observer has traffic data for our app.
	helpers.RequireEventually(t, 180*time.Second, func() bool {
		end := fmt.Sprintf("%d", time.Now().Unix())
		result, ok := observerTryGet(t, "/v1/traffic", map[string]string{
			"namespace": envNs,
			"app":       app.Name,
			"env":       envName,
			"start":     start,
			"end":       end,
			"step":      "5",
		})
		if !ok {
			return false
		}

		series, ok := result["series"].(map[string]any)
		if !ok {
			return false
		}
		requests, ok := series["requests"].([]any)
		return ok && len(requests) > 0
	})

	end := fmt.Sprintf("%d", time.Now().Unix())
	result := observerGet(t, "/v1/traffic", map[string]string{
		"namespace": envNs,
		"app":       app.Name,
		"env":       envName,
		"start":     start,
		"end":       end,
		"step":      "5",
	})

	series := result["series"].(map[string]any)
	requests := series["requests"].([]any)
	if len(requests) == 0 {
		t.Fatal("expected at least one traffic data point")
	}

	// Sum total requests across all buckets.
	var totalRequests float64
	for _, pt := range requests {
		point := pt.([]any)
		totalRequests += point[1].(float64)
	}
	if totalRequests <= 0 {
		t.Errorf("expected total requests > 0, got %f", totalRequests)
	}

	// Status 2xx should have data (nginx returns 200).
	s2xx := series["status2xx"].([]any)
	if len(s2xx) == 0 {
		t.Error("expected status2xx data points")
	}
}

func TestObserverTrafficMissingParams(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/traffic?namespace=foo", observerLocalPort))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for missing params, got %d", resp.StatusCode)
	}
}

func TestObserverTrafficEmptyRange(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	result := observerGet(t, "/v1/traffic", map[string]string{
		"namespace": "pj-nonexistent-production",
		"app":       "nope",
		"env":       "production",
		"start":     "0",
		"end":       "1",
		"step":      "5",
	})

	series, ok := result["series"].(map[string]any)
	if !ok {
		t.Fatal("expected series object in response")
	}
	requests, _ := series["requests"].([]any)
	if len(requests) != 0 {
		t.Fatalf("expected empty requests, got %d", len(requests))
	}
}

func TestObserverMetricsAfterRedeploy(t *testing.T) {
	t.Parallel()
	skipIfObserverUnavailable(t)

	projectName := "obs-redeploy-" + randSuffix()
	ns := createProjectForTest(t, projectName)

	app := helpers.LoadFixture(t, filepath.Join(fixturesDir(), "image-basic.yaml"))
	app.Namespace = ns
	app.Name = "redeploy-app"

	if err := k8sClient.Create(context.Background(), app); err != nil {
		t.Fatalf("create app: %v", err)
	}

	envName := app.Spec.Environments[0].Name
	envNs := constants.EnvNamespace(projectName, envName)

	helpers.WaitForAppReady(t, k8sClient, ns, app.Name, 2*time.Minute)
	helpers.AssertPodsRunning(t, k8sClient, envNs, app.Name, 1)

	// Fail fast if the metrics collector never ticks (dead); otherwise give
	// metrics-server warmup + scrape headroom on loaded runners (mo-90d).
	requireCollectorAlive(t, "metrics")

	// Wait for observer to collect metrics from the initial deployment.
	start := fmt.Sprintf("%d", time.Now().Add(-1*time.Minute).Unix())
	helpers.RequireEventually(t, 180*time.Second, func() bool {
		end := fmt.Sprintf("%d", time.Now().Unix())
		result, ok := observerTryGet(t, "/v1/metrics", map[string]string{
			"namespace": envNs,
			"app":       app.Name,
			"env":       envName,
			"start":     start,
			"end":       end,
			"step":      "5",
		})
		if !ok {
			return false
		}
		pods, ok := result["pods"].([]any)
		return ok && len(pods) > 0
	})

	// Record the pre-deploy pod name.
	var preDep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: envNs,
	}, &preDep); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	preGeneration := preDep.Status.ObservedGeneration

	// Patch the image to trigger a rolling update.
	var live mortisev1alpha1.App
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: ns,
	}, &live); err != nil {
		t.Fatalf("get app for patch: %v", err)
	}
	patch := client.MergeFrom(live.DeepCopy())
	live.Spec.Source.Image = "redis:7"
	if err := k8sClient.Patch(context.Background(), &live, patch); err != nil {
		t.Fatalf("patch app image: %v", err)
	}

	// Wait for the rolling update to complete: new generation, ready replicas.
	helpers.RequireEventually(t, 2*time.Minute, func() bool {
		var dep appsv1.Deployment
		if err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: app.Name, Namespace: envNs,
		}, &dep); err != nil {
			return false
		}
		return dep.Status.ObservedGeneration > preGeneration &&
			dep.Status.ReadyReplicas == 1 &&
			dep.Status.UpdatedReplicas == 1
	})

	// Record the new pod name from the updated Deployment's ReplicaSet.
	var postDep appsv1.Deployment
	if err := k8sClient.Get(context.Background(), types.NamespacedName{
		Name: app.Name, Namespace: envNs,
	}, &postDep); err != nil {
		t.Fatalf("get post-deploy deployment: %v", err)
	}

	// Poll until the observer returns metrics referencing the new pod. The
	// metrics collector was already proven alive above, so this only needs the
	// same loaded-runner headroom, not another liveness gate (mo-90d).
	redeployStart := fmt.Sprintf("%d", time.Now().Add(-1*time.Minute).Unix())
	helpers.RequireEventually(t, 180*time.Second, func() bool {
		end := fmt.Sprintf("%d", time.Now().Unix())
		result, ok := observerTryGet(t, "/v1/metrics", map[string]string{
			"namespace": envNs,
			"app":       app.Name,
			"env":       envName,
			"start":     redeployStart,
			"end":       end,
			"step":      "5",
		})
		if !ok {
			return false
		}
		pods, ok := result["pods"].([]any)
		if !ok || len(pods) == 0 {
			return false
		}
		for _, p := range pods {
			pod := p.(map[string]any)
			mem, ok := pod["memory"].([]any)
			if ok && len(mem) > 0 {
				return true
			}
		}
		return false
	})

	end := fmt.Sprintf("%d", time.Now().Unix())
	result := observerGet(t, "/v1/metrics", map[string]string{
		"namespace": envNs,
		"app":       app.Name,
		"env":       envName,
		"start":     redeployStart,
		"end":       end,
		"step":      "5",
	})

	pods := result["pods"].([]any)
	if len(pods) == 0 {
		t.Fatal("expected metrics for post-deploy pod")
	}

	pod := pods[0].(map[string]any)
	podName, _ := pod["name"].(string)
	if podName == "" {
		t.Fatal("post-deploy pod name is empty")
	}

	mem := pod["memory"].([]any)
	if len(mem) == 0 {
		t.Fatal("expected memory data for post-deploy pod")
	}
	point := mem[0].([]any)
	memVal := point[1].(float64)
	if memVal <= 0 {
		t.Errorf("expected post-deploy memory > 0, got %f", memVal)
	}
}
