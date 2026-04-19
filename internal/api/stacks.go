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

	if req.Template != "" {
		tpl, err := resolveTemplate(req.Template, req.Vars)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
			return
		}
		composeYAML = tpl
		if stackPrefix == "" {
			stackPrefix = req.Template
		}
	}

	cf, err := parseCompose(composeYAML)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	specs, err := composeToAppSpecs(cf, stackPrefix)
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

// resolveTemplate returns the compose YAML for a built-in template.
func resolveTemplate(name string, vars map[string]string) (string, error) {
	switch name {
	case "supabase":
		return substituteVars(supabaseTemplate, vars)
	default:
		return "", fmt.Errorf("unknown template %q", name)
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
`
