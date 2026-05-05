//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/test/helpers"
)

func observerURL(path string, params map[string]string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d%s", observerLocalPort, path)
	if len(params) > 0 {
		u += "?"
		first := true
		for k, v := range params {
			if !first {
				u += "&"
			}
			u += k + "=" + v
			first = false
		}
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

	// Poll until the observer has collected at least one metrics data point.
	helpers.RequireEventually(t, 90*time.Second, func() bool {
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

	// Poll until the observer has collected at least one log line.
	helpers.RequireEventually(t, 90*time.Second, func() bool {
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

	// Poll until the observer has traffic data for our app.
	helpers.RequireEventually(t, 120*time.Second, func() bool {
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
