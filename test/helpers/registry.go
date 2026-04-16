package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// AssertRegistryHasTags polls the OCI-compliant registry at baseURL for tags
// under namespace/app until at least one tag is present, or timeout elapses.
// baseURL should be something like "http://127.0.0.1:53812" (typically backed
// by a kubectl port-forward into in-cluster Zot).
//
// Uses only the OCI Distribution Spec endpoint (GET /v2/<name>/tags/list);
// no Zot-specific APIs.
func AssertRegistryHasTags(t *testing.T, baseURL, namespace, app string, timeout time.Duration) []string {
	t.Helper()

	var tags []string
	endpoint := fmt.Sprintf("%s/v2/%s/%s/tags/list", baseURL, namespace, app)

	RequireEventually(t, timeout, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var payload struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return false
		}
		if len(payload.Tags) == 0 {
			return false
		}
		tags = payload.Tags
		return true
	})

	return tags
}
