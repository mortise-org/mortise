package api

import (
	"io"
	"net/http"
	"net/url"
	"time"
)

var adapterClient = &http.Client{Timeout: 5 * time.Second}

func (s *Server) proxyToAdapter(w http.ResponseWriter, adapterURL, token string, query url.Values) {
	u, err := url.Parse(adapterURL)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true,
			"error":     "invalid adapter endpoint",
			"detail":    err.Error(),
		})
		return
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true,
			"error":     "failed to build adapter request",
			"detail":    err.Error(),
		})
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := adapterClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true,
			"error":     "adapter unreachable",
			"detail":    err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		writeJSON(w, http.StatusOK, map[string]any{
			"available": true,
			"error":     "adapter returned " + resp.Status,
			"detail":    string(body),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}
