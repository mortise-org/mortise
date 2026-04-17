package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func defaultHTTPClient() *http.Client {
	return http.DefaultClient
}

// Client wraps HTTP calls to the Mortise API server.
//
// URL scheme (mirrors internal/api/server.go):
//
//	/api/projects
//	/api/projects/{project}
//	/api/projects/{project}/apps
//	/api/projects/{project}/apps/{app}
//	/api/projects/{project}/apps/{app}/deploy
//	/api/projects/{project}/apps/{app}/logs
//	/api/projects/{project}/apps/{app}/secrets
//	/api/projects/{project}/apps/{app}/secrets/{secretName}
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client

	// currentProject is the default project used when callers pass "" to
	// ResolveProject. It's populated from config at construction time.
	currentProject string
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
		BaseURL:        cfg.ServerURL,
		Token:          cfg.Token,
		HTTPClient:     http.DefaultClient,
		currentProject: cfg.Project(),
	}, nil
}

// ResolveProject returns flagValue when non-empty, otherwise the client's
// current project (from config). Every app-scoped command funnels through
// this so `--project` consistently overrides and config is the fallback.
func (c *Client) ResolveProject(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return c.currentProject
}

// projectBase returns the URL prefix for a given project's resources.
func (c *Client) projectBase(project string) string {
	return fmt.Sprintf("%s/api/projects/%s", c.BaseURL, url.PathEscape(project))
}

// appBase returns the URL prefix for a given app's resources inside a project.
func (c *Client) appBase(project, app string) string {
	return fmt.Sprintf("%s/apps/%s", c.projectBase(project), url.PathEscape(app))
}

func (c *Client) do(method, fullURL string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, fullURL, reqBody)
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

func (c *Client) doJSON(method, fullURL string, body, dest any) error {
	resp, err := c.do(method, fullURL, body)
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

func (c *Client) doRaw(method, fullURL, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.HTTPClient.Do(req)
}

// ---------- Project methods ----------

// ProjectResponse mirrors internal/api.projectResponse.
type ProjectResponse struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Namespace   string `json:"namespace"`
	Phase       string `json:"phase,omitempty"`
	AppCount    int32  `json:"appCount"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

type createProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (c *Client) ListProjects() ([]ProjectResponse, error) {
	var resp []ProjectResponse
	if err := c.doJSON(http.MethodGet, c.BaseURL+"/api/projects", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) CreateProject(name, description string) (*ProjectResponse, error) {
	var resp ProjectResponse
	req := createProjectRequest{Name: name, Description: description}
	if err := c.doJSON(http.MethodPost, c.BaseURL+"/api/projects", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetProject(name string) (*ProjectResponse, error) {
	var resp ProjectResponse
	u := fmt.Sprintf("%s/api/projects/%s", c.BaseURL, url.PathEscape(name))
	if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) DeleteProject(name string) error {
	u := fmt.Sprintf("%s/api/projects/%s", c.BaseURL, url.PathEscape(name))
	return c.doJSON(http.MethodDelete, u, nil, nil)
}

// ---------- App methods ----------

// CreateAppRequest is the body for POST /api/projects/{project}/apps. The
// server wraps {name, spec}; we mirror that shape here so callers build a
// full AppSpec rather than guessing at flat fields.
type CreateAppRequest struct {
	Name string                  `json:"name"`
	Spec mortisev1alpha1.AppSpec `json:"spec"`
}

func (c *Client) ListApps(project string) ([]mortisev1alpha1.App, error) {
	var resp []mortisev1alpha1.App
	u := c.projectBase(project) + "/apps"
	if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) CreateApp(project string, req CreateAppRequest) (*mortisev1alpha1.App, error) {
	var app mortisev1alpha1.App
	u := c.projectBase(project) + "/apps"
	if err := c.doJSON(http.MethodPost, u, req, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) GetApp(project, name string) (*mortisev1alpha1.App, error) {
	var app mortisev1alpha1.App
	if err := c.doJSON(http.MethodGet, c.appBase(project, name), nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) DeleteApp(project, name string) error {
	return c.doJSON(http.MethodDelete, c.appBase(project, name), nil, nil)
}

// ---------- Deploy ----------

type deployRequest struct {
	Environment string `json:"environment"`
	Image       string `json:"image"`
}

func (c *Client) Deploy(project, app, env, image string) error {
	u := c.appBase(project, app) + "/deploy"
	return c.doJSON(http.MethodPost, u, deployRequest{Environment: env, Image: image}, nil)
}

// ---------- Rollback ----------

type rollbackRequest struct {
	Environment string `json:"environment"`
	Index       int    `json:"index"`
}

func (c *Client) Rollback(project, app, env string, index int) (*mortisev1alpha1.DeployRecord, error) {
	var record mortisev1alpha1.DeployRecord
	u := c.appBase(project, app) + "/rollback"
	if err := c.doJSON(http.MethodPost, u, rollbackRequest{Environment: env, Index: index}, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// ---------- Promote ----------

type promoteRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (c *Client) Promote(project, app, from, to string) error {
	u := c.appBase(project, app) + "/promote"
	return c.doJSON(http.MethodPost, u, promoteRequest{From: from, To: to}, nil)
}

// ---------- Logs ----------

// StreamLogs opens the SSE log stream for (project, app) and copies the raw
// response body to w. Callers may cancel via context on the underlying
// HTTPClient; this helper is intentionally minimal — it just proxies bytes.
func (c *Client) StreamLogs(project, app, env string, w io.Writer) error {
	u := c.appBase(project, app) + "/logs?follow=true"
	if env != "" {
		u += "&env=" + url.QueryEscape(env)
	}
	resp, err := c.do(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// ---------- Secrets ----------

// SecretResponse mirrors internal/api.secretResponse.
type SecretResponse struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

type createSecretRequest struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}

func (c *Client) ListSecrets(project, app string) ([]SecretResponse, error) {
	var resp []SecretResponse
	u := c.appBase(project, app) + "/secrets"
	if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// SetSecret upserts a single-key secret named `name` with value `value`.
// The server accepts a map; single-key is the common CLI shape
// (`mortise secret set my-app API_KEY=xxx`).
func (c *Client) SetSecret(project, app, name, value string) error {
	u := c.appBase(project, app) + "/secrets"
	req := createSecretRequest{Name: name, Data: map[string]string{name: value}}
	return c.doJSON(http.MethodPost, u, req, nil)
}

func (c *Client) DeleteSecret(project, app, secretName string) error {
	u := fmt.Sprintf("%s/secrets/%s", c.appBase(project, app), url.PathEscape(secretName))
	return c.doJSON(http.MethodDelete, u, nil, nil)
}

// ---------- Deploy Tokens ----------

// TokenResponse mirrors internal/api.tokenResponse.
type TokenResponse struct {
	Token       string `json:"token,omitempty"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

type cliCreateTokenRequest struct {
	Environment string `json:"environment"`
	Name        string `json:"name"`
}

func (c *Client) CreateToken(project, app, env, name string) (*TokenResponse, error) {
	var resp TokenResponse
	u := c.appBase(project, app) + "/tokens"
	req := cliCreateTokenRequest{Environment: env, Name: name}
	if err := c.doJSON(http.MethodPost, u, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ListTokens(project, app string) ([]TokenResponse, error) {
	var resp []TokenResponse
	u := c.appBase(project, app) + "/tokens"
	if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) RevokeToken(project, app, name string) error {
	u := fmt.Sprintf("%s/tokens/%s", c.appBase(project, app), url.PathEscape(name))
	return c.doJSON(http.MethodDelete, u, nil, nil)
}

// ---------- Env Vars ----------

// EnvVarResponse mirrors internal/api.envVarResponse.
type EnvVarResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type cliPatchEnvRequest struct {
	Set   map[string]string `json:"set,omitempty"`
	Unset []string          `json:"unset,omitempty"`
}

func (c *Client) GetEnv(project, app, env string) ([]EnvVarResponse, error) {
	var resp []EnvVarResponse
	u := c.appBase(project, app) + "/env?environment=" + url.QueryEscape(env)
	if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) PatchEnv(project, app, env string, set map[string]string, unset []string) error {
	u := c.appBase(project, app) + "/env?environment=" + url.QueryEscape(env)
	req := cliPatchEnvRequest{Set: set, Unset: unset}
	return c.doJSON(http.MethodPatch, u, req, nil)
}

func (c *Client) ImportEnv(project, app, env, content string) error {
	u := c.appBase(project, app) + "/env/import?environment=" + url.QueryEscape(env)
	resp, err := c.doRaw(http.MethodPost, u, "text/plain", strings.NewReader(content))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
