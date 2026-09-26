package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A full-replace PUT that drops a key must say so in the activity log by
// name — "Updated env vars" alone left a silent deletion untraceable
// (CAI-350). Values never appear.
func TestPutEnvActivityNamesAddedAndRemovedKeys(t *testing.T) {
	k8sClient := setupEnvtest(t)
	srv := newAdminServer(t, k8sClient)
	h := srv.Handler()
	seedProject(t, k8sClient, "default")
	seedAppForEnv(t, h)

	put := func(vars []map[string]string) {
		t.Helper()
		w := doRequest(h, http.MethodPut, "/api/projects/default/apps/webapp/env?environment=production", vars)
		if w.Code != http.StatusOK {
			t.Fatalf("put: expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	put([]map[string]string{{"name": "KEEP", "value": "v1"}, {"name": "DROP_ME", "value": "secret-value"}})
	put([]map[string]string{{"name": "KEEP", "value": "v2"}, {"name": "FRESH", "value": "v3"}})

	w := doRequest(h, http.MethodGet, "/api/projects/default/activity", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("activity: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	var entries []struct {
		Msg string `json:"msg"`
	}
	_ = json.NewDecoder(strings.NewReader(body)).Decode(&entries)
	var msg string
	for _, e := range entries {
		if strings.Contains(e.Msg, "added: FRESH") {
			msg = e.Msg
			break
		}
	}
	if msg == "" {
		t.Fatalf("no activity entry naming the added key; body: %s", body)
	}
	if !strings.Contains(msg, "removed: DROP_ME") {
		t.Errorf("dropped key not named: %q", msg)
	}
	if strings.Contains(body, "secret-value") || strings.Contains(body, "v3") {
		t.Errorf("activity body leaked a value: %s", body)
	}
}
