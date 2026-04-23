package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxyToAdapter_Success(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("app") != "myapp" {
			t.Errorf("expected app=myapp, got %s", r.URL.Query().Get("app"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pods":[{"name":"pod-1","cpu":[[1700000000,0.25]]}]}`))
	}))
	defer adapter.Close()

	s := &Server{}
	w := httptest.NewRecorder()
	s.proxyToAdapter(w, httptest.NewRequest("GET", "/", nil), adapter.URL+"/v1/metrics", "", url.Values{"app": {"myapp"}})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if _, ok := result["pods"]; !ok {
		t.Errorf("expected pods in response, got %v", result)
	}
}

func TestProxyToAdapter_WithToken(t *testing.T) {
	var gotAuth string
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer adapter.Close()

	s := &Server{}
	w := httptest.NewRecorder()
	s.proxyToAdapter(w, httptest.NewRequest("GET", "/", nil), adapter.URL+"/v1/logs", "my-token", url.Values{})

	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization = %q, want Bearer my-token", gotAuth)
	}
}

func TestProxyToAdapter_AdapterError(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("service unavailable"))
	}))
	defer adapter.Close()

	s := &Server{}
	w := httptest.NewRecorder()
	s.proxyToAdapter(w, httptest.NewRequest("GET", "/", nil), adapter.URL+"/v1/metrics", "", url.Values{})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (error wrapped in JSON)", w.Code)
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["error"] == nil {
		t.Error("expected error field in response")
	}
}

func TestProxyToAdapter_Unreachable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.proxyToAdapter(w, httptest.NewRequest("GET", "/", nil), "http://127.0.0.1:1/v1/metrics", "", url.Values{})

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["error"] != "adapter unreachable" {
		t.Errorf("error = %v, want 'adapter unreachable'", result["error"])
	}
}
