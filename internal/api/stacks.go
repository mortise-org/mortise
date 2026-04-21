package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
	"github.com/MC-Meesh/mortise/internal/envstore"
	"github.com/MC-Meesh/mortise/internal/templates"
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
	ns, project, ok := s.resolveProject(w, r)
	if !ok {
		return
	}

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
		tpl, err := templates.Load(req.Template)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
			return
		}
		composed, err := substituteVars(tpl.Compose, req.Vars)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
			return
		}
		composeYAML = composed
		bundledFiles = tpl.Files
		if stackPrefix == "" {
			stackPrefix = tpl.Name
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

	// Write auto-generated template vars to the control namespace.
	// The controller materializes these into shared-env in each env namespace
	// during reconcile — no race condition since the control ns always exists.
	if len(req.Vars) > 0 {
		controlNs := constants.ControlNamespace(project)
		store := &envstore.Store{Client: s.client}
		var sharedVars []envstore.Env
		for k, v := range req.Vars {
			sharedVars = append(sharedVars, envstore.Env{
				Name:   k,
				Value:  v,
				Source: "generated",
			})
		}
		labels := map[string]string{
			constants.ProjectLabel:  project,
			"mortise.dev/stack":     stackPrefix,
		}
		if err := store.MergeSharedSource(r.Context(), controlNs, sharedVars, labels); err != nil {
			log.Printf("warning: failed to persist shared vars to control namespace: %v", err)
		}
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
	Name     string        `json:"name"`
	Services []serviceInfo `json:"services"`
}

type serviceInfo struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	names, err := templates.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to list templates: " + err.Error()})
		return
	}

	var result []templateInfo
	for _, name := range names {
		tpl, err := templates.Load(name)
		if err != nil {
			continue
		}
		// Parse the compose to extract service info.
		cf, err := parseCompose(tpl.Compose)
		if err != nil {
			continue
		}
		var services []serviceInfo
		for svcName, svc := range cf.Services {
			services = append(services, serviceInfo{
				Name:  svcName,
				Image: svc.Image,
			})
		}
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		result = append(result, templateInfo{
			Name:     tpl.Name,
			Services: services,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// substituteVars replaces ${VAR_NAME} placeholders in the compose YAML.
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
