package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

type createStackRequest struct {
	// Compose is raw docker-compose YAML. Mutually exclusive with Template.
	Compose string `json:"compose,omitempty"`
	// Template selects a built-in compose template (e.g. "supabase").
	Template string `json:"template,omitempty"`
	// Name is an optional prefix for app names.
	Name string `json:"name,omitempty"`
	// Vars are variable substitutions for the template.
	Vars map[string]string `json:"vars,omitempty"`
	// Services filters the compose to only the named services.
	// If empty, all services are included.
	Services []string `json:"services,omitempty"`
}

type createStackResponse struct {
	Apps []string `json:"apps"`
}

func (s *Server) CreateStack(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	project := chi.URLParam(r, "project")

	var req createStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	if req.Compose == "" && req.Template == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"one of compose or template is required"})
		return
	}
	if req.Compose != "" && req.Template != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"compose and template are mutually exclusive"})
		return
	}

	composeYAML := req.Compose
	stackPrefix := req.Name
	var bundledFiles map[string]string

	if req.Template != "" {
		bundle, err := resolveTemplate(req.Template, req.Vars)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
			return
		}
		composeYAML = bundle.Compose
		bundledFiles = bundle.Files
		if stackPrefix == "" {
			stackPrefix = req.Template
		}
	}

	cf, err := parseCompose(composeYAML)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	// Filter to selected services (if specified).
	if len(req.Services) > 0 {
		keep := make(map[string]bool, len(req.Services))
		for _, s := range req.Services {
			keep[s] = true
		}
		for name := range cf.Services {
			if !keep[name] {
				delete(cf.Services, name)
			}
		}
		// Also prune depends_on references to excluded services.
		for name, svc := range cf.Services {
			svc.DependsOn = filterDependsOn(svc.DependsOn, keep)
			cf.Services[name] = svc
		}
	}

	specs, err := composeToAppSpecs(cf, stackPrefix, bundledFiles)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	// Stamp creator annotation.
	annotations := map[string]string{}
	if p := PrincipalFromContext(r.Context()); p != nil {
		annotations["mortise.dev/created-by"] = p.Email
	}

	var created []string
	for _, as := range specs {
		if as.IsInit {
			continue
		}

		if msg := validateDNSLabel("name", as.Name, maxAppNameLen); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{fmt.Sprintf("service %s: %s", as.Service, msg)})
			return
		}

		app := &mortisev1alpha1.App{
			ObjectMeta: metav1.ObjectMeta{
				Name:      as.Name,
				Namespace: ns,
				Labels: map[string]string{
					"mortise.dev/stack":   stackPrefix,
					"mortise.dev/project": project,
				},
				Annotations: annotations,
			},
			Spec: as.Spec,
		}

		if err := s.client.Create(r.Context(), app); err != nil {
			writeError(w, err)
			return
		}
		created = append(created, as.Name)
	}

	writeJSON(w, http.StatusCreated, createStackResponse{Apps: created})
}

// filterDependsOn removes references to services not in the keep set.
func filterDependsOn(v interface{}, keep map[string]bool) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []interface{}:
		var out []interface{}
		for _, item := range val {
			if s, ok := item.(string); ok && keep[s] {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{})
		for k, v := range val {
			if keep[k] {
				out[k] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

type templateInfo struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Services    []serviceInfo `json:"services"`
}

type serviceInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
	Required    bool   `json:"required"`
}

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates := []templateInfo{
		{
			Name:        "supabase",
			Description: "Self-hosted Supabase (auth, database, storage, realtime, API gateway)",
			Services: []serviceInfo{
				{Name: "postgres", Description: "PostgreSQL database", Image: "supabase/postgres:15.6.1.143", Required: true},
				{Name: "auth", Description: "GoTrue authentication", Image: "supabase/gotrue:v2.164.0", Required: false},
				{Name: "rest", Description: "PostgREST API", Image: "postgrest/postgrest:v12.2.3", Required: false},
				{Name: "storage", Description: "File storage API", Image: "supabase/storage-api:v1.11.13", Required: false},
				{Name: "realtime", Description: "Realtime WebSocket server", Image: "supabase/realtime:v2.85.2", Required: false},
				{Name: "studio", Description: "Supabase dashboard UI", Image: "supabase/studio:2026.04.13-sha-e95f1cc", Required: false},
			},
		},
	}
	writeJSON(w, http.StatusOK, templates)
}

// templateBundle is a docker-compose YAML with any referenced files bundled.
type templateBundle struct {
	Compose string            // docker-compose.yml content
	Files   map[string]string // host path -> file content (for volume mounts)
}

// resolveTemplate returns the compose YAML and bundled files for a built-in template.
func resolveTemplate(name string, vars map[string]string) (*templateBundle, error) {
	switch name {
	case "supabase":
		composed, err := substituteVars(supabaseTemplate, vars)
		if err != nil {
			return nil, err
		}
		return &templateBundle{
			Compose: composed,
			Files: map[string]string{
				"./volumes/db/init/00-init.sql": supabaseInitSQL,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown template %q", name)
	}
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// substituteVars replaces ${VAR_NAME} placeholders in the template.
// Any unresolved variables are auto-generated as random 32-char hex strings.
// This means templates "just work" without requiring the user to provide secrets.
func substituteVars(tpl string, vars map[string]string) (string, error) {
	if vars == nil {
		vars = make(map[string]string)
	}
	result := tpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}
	// Auto-generate any remaining unresolved variables.
	for {
		idx := strings.Index(result, "${")
		if idx == -1 {
			break
		}
		end := strings.Index(result[idx:], "}")
		if end < 0 {
			break
		}
		varName := result[idx+2 : idx+end]
		generated := generateRandomHex(16)
		vars[varName] = generated
		result = strings.ReplaceAll(result, "${"+varName+"}", generated)
	}
	return result, nil
}

const supabaseTemplate = `services:
  postgres:
    image: supabase/postgres:15.6.1.143
    ports: ["5432:5432"]
    volumes:
      - ./volumes/db/init/00-init.sql:/docker-entrypoint-initdb.d/00-init.sql
    environment:
      POSTGRES_PASSWORD: ${PG_PASSWORD}
      POSTGRES_DB: supabase
      JWT_SECRET: ${JWT_SECRET}
      SUPABASE_AUTH_ADMIN_PASSWORD: ${PG_PASSWORD}
      SUPABASE_STORAGE_ADMIN_PASSWORD: ${PG_PASSWORD}

  auth:
    image: supabase/gotrue:v2.164.0
    depends_on: [postgres]
    ports: ["9999:9999"]
    environment:
      GOTRUE_DB_DRIVER: postgres
      GOTRUE_DB_DATABASE_URL: postgres://supabase_admin:${PG_PASSWORD}@supabase-postgres-production:5432/supabase?sslmode=disable
      DATABASE_URL: postgres://supabase_admin:${PG_PASSWORD}@supabase-postgres-production:5432/supabase?sslmode=disable
      GOTRUE_JWT_SECRET: ${JWT_SECRET}
      GOTRUE_JWT_EXP: "3600"
      GOTRUE_SITE_URL: http://localhost
      API_EXTERNAL_URL: http://localhost
      GOTRUE_API_HOST: 0.0.0.0
      GOTRUE_EXTERNAL_EMAIL_ENABLED: "true"
      GOTRUE_MAILER_AUTOCONFIRM: "true"
      GOTRUE_DISABLE_SIGNUP: "false"
      PORT: "9999"

  rest:
    image: postgrest/postgrest:v12.2.3
    depends_on: [postgres]
    ports: ["3000:3000"]
    environment:
      PGRST_DB_URI: postgres://supabase_admin:${PG_PASSWORD}@supabase-postgres-production:5432/supabase
      PGRST_DB_SCHEMA: public,storage
      PGRST_DB_ANON_ROLE: anon
      PGRST_JWT_SECRET: ${JWT_SECRET}
      PGRST_DB_USE_LEGACY_GUCS: "false"

  storage:
    image: supabase/storage-api:v1.11.13
    depends_on: [postgres]
    ports: ["5000:5000"]
    environment:
      DATABASE_URL: postgres://supabase_admin:${PG_PASSWORD}@supabase-postgres-production:5432/supabase
      PGRST_JWT_SECRET: ${JWT_SECRET}
      ANON_KEY: ${JWT_SECRET}
      SERVICE_KEY: ${JWT_SECRET}
      STORAGE_BACKEND: file
      FILE_STORAGE_BACKEND_PATH: /var/lib/storage

  realtime:
    image: supabase/realtime:v2.85.2
    depends_on: [postgres]
    ports: ["4000:4000"]
    environment:
      DB_HOST: supabase-postgres-production
      DB_PORT: "5432"
      DB_USER: supabase_admin
      DB_PASSWORD: ${PG_PASSWORD}
      DB_NAME: supabase
      JWT_SECRET: ${JWT_SECRET}
      API_JWT_SECRET: ${JWT_SECRET}
      APP_NAME: realtime
      SELF_HOSTED: "true"
      PORT: "4000"
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
      METRICS_JWT_SECRET: ${JWT_SECRET}
      ERL_AFLAGS: "-proto_dist inet_tcp"
      RLIMIT_NOFILE: "10000"
      FLY_ALLOC_ID: fly123
      FLY_APP_NAME: realtime
      DNS_NODES: "''"

  studio:
    image: supabase/studio:2026.04.13-sha-e95f1cc
    depends_on: [postgres]
    ports: ["3001:3000"]
    environment:
      STUDIO_PG_META_URL: http://supabase-rest-production:3000
      POSTGRES_PASSWORD: ${PG_PASSWORD}
      SUPABASE_URL: http://supabase-rest-production:3000
      SUPABASE_REST_URL: http://supabase-rest-production:3000/rest/v1
      SUPABASE_ANON_KEY: ${ANON_KEY}
      SUPABASE_SERVICE_ROLE_KEY: ${SERVICE_ROLE_KEY}
      DEFAULT_ORGANIZATION_NAME: Default Organization
      DEFAULT_PROJECT_NAME: Default Project
      PORT: "3000"
`

// supabaseInitSQL is bundled alongside the compose template.
// It's referenced by the compose volumes entry and resolved by the generic
// compose parser — no Supabase-specific Go logic needed.
const supabaseInitSQL = `-- GoTrue requires these enum types and schemas before its migrations run
CREATE SCHEMA IF NOT EXISTS auth;
CREATE SCHEMA IF NOT EXISTS storage;
CREATE SCHEMA IF NOT EXISTS realtime;
DO $$ BEGIN CREATE TYPE auth.factor_type AS ENUM ('totp', 'webauthn'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.factor_status AS ENUM ('unverified', 'verified'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.aal_level AS ENUM ('aal1', 'aal2', 'aal3'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.code_challenge_method AS ENUM ('s256', 'plain'); EXCEPTION WHEN duplicate_object THEN null; END $$;
DO $$ BEGIN CREATE TYPE auth.one_time_token_type AS ENUM ('confirmation_token', 'reauthentication_token', 'recovery_token', 'email_change_token_new', 'email_change_token_current', 'phone_change_token'); EXCEPTION WHEN duplicate_object THEN null; END $$;
`
