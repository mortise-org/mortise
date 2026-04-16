package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func defaultHTTPClient() *http.Client {
	return http.DefaultClient
}

// Client wraps HTTP calls to the Mortise API server.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func newClientFromConfig() (*Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("server_url not configured; run 'mortise login' first")
	}
	return &Client{
		BaseURL:    cfg.ServerURL,
		Token:      cfg.Token,
		HTTPClient: http.DefaultClient,
	}, nil
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.HTTPClient.Do(req)
}

func (c *Client) doJSON(method, path string, body, dest any) error {
	resp, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}
	if dest != nil {
		return json.NewDecoder(resp.Body).Decode(dest)
	}
	return nil
}
