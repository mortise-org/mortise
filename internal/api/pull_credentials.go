package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/constants"
)

// pullCredentialsRequest is the JSON body for setting pull credentials.
type pullCredentialsRequest struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// pullCredentialsResponse is the JSON response for GET pull-credentials.
// The password is never returned.
type pullCredentialsResponse struct {
	Registry    string `json:"registry"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
}

// pullSecretName returns the Mortise-managed pull secret name for an app.
func pullSecretName(appName string) string {
	return appName + "-pull-secret"
}

type dockerConfigJSON struct {
	Auths map[string]dockerConfigAuth `json:"auths"`
}

type dockerConfigAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

// buildDockerConfigJSON builds the `.dockerconfigjson` payload for a
// kubernetes.io/dockerconfigjson Secret.
func buildDockerConfigJSON(registry, username, password string) []byte {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	cfg := dockerConfigJSON{
		Auths: map[string]dockerConfigAuth{
			registry: {
				Username: username,
				Password: password,
				Auth:     auth,
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return b
}

// SetPullCredentials creates or updates a dockerconfigjson Secret in every env
// namespace for the app, then sets PullSecretRef on the App CRD.
func (s *Server) SetPullCredentials(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var req pullCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}
	if req.Registry == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"registry is required"})
		return
	}
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"username is required"})
		return
	}
	if req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"password is required"})
		return
	}

	// Verify the app exists.
	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	// Get project to find all environments.
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	secretName := pullSecretName(appName)
	dockerJSON := buildDockerConfigJSON(req.Registry, req.Username, req.Password)

	// Create or update the pull secret in each env namespace.
	for _, env := range project.Spec.Environments {
		envNs := constants.EnvNamespace(projectName, env.Name)
		if err := s.upsertPullSecret(r, envNs, secretName, appName, dockerJSON); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to create pull secret in " + envNs + ": " + err.Error()})
			return
		}
	}

	// Set PullSecretRef on the App CRD.
	app.Spec.Source.PullSecretRef = secretName
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	s.recordActivity(r, projectName, "update", "app", appName,
		"Set pull credentials for "+appName+" (registry: "+req.Registry+")", "")

	writeJSON(w, http.StatusOK, pullCredentialsResponse{
		Registry:    req.Registry,
		Username:    req.Username,
		HasPassword: true,
	})
}

// GetPullCredentials returns pull credential metadata (never the password).
func (s *Server) GetPullCredentials(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionRead) {
		return
	}
	appName := chi.URLParam(r, "app")

	// Get project to find an env namespace to read the secret from.
	project, ok := s.getProject(w, r)
	if !ok {
		return
	}
	if len(project.Spec.Environments) == 0 {
		writeJSON(w, http.StatusOK, pullCredentialsResponse{})
		return
	}

	secretName := pullSecretName(appName)
	envNs := constants.EnvNamespace(projectName, project.Spec.Environments[0].Name)

	var secret corev1.Secret
	err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: envNs}, &secret)
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusOK, pullCredentialsResponse{})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}

	// Only report on Mortise-managed secrets.
	if secret.Labels["app.kubernetes.io/managed-by"] != "mortise" {
		writeJSON(w, http.StatusOK, pullCredentialsResponse{})
		return
	}

	// Parse the dockerconfigjson to extract registry and username.
	resp := parsePullCredentials(&secret)
	writeJSON(w, http.StatusOK, resp)
}

// DeletePullCredentials removes the Mortise-managed pull secret from all env
// namespaces and clears PullSecretRef on the App CRD.
func (s *Server) DeletePullCredentials(w http.ResponseWriter, r *http.Request) {
	ns, projectName, ok := s.resolveProject(w, r)
	if !ok {
		return
	}
	if !s.authorize(w, r, authz.Resource{Kind: "app", Namespace: ns, Project: projectName}, authz.ActionUpdate) {
		return
	}
	appName := chi.URLParam(r, "app")

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: appName, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	project, ok := s.getProject(w, r)
	if !ok {
		return
	}

	secretName := pullSecretName(appName)

	// Delete the pull secret from each env namespace.
	for _, env := range project.Spec.Environments {
		envNs := constants.EnvNamespace(projectName, env.Name)
		var secret corev1.Secret
		err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: envNs}, &secret)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to get pull secret: " + err.Error()})
			return
		}
		if secret.Labels["app.kubernetes.io/managed-by"] != "mortise" {
			continue
		}
		if err := s.client.Delete(r.Context(), &secret); err != nil && !errors.IsNotFound(err) {
			writeJSON(w, http.StatusInternalServerError, errorResponse{"failed to delete pull secret: " + err.Error()})
			return
		}
	}

	// Clear PullSecretRef only if it points to the Mortise-managed secret.
	if app.Spec.Source.PullSecretRef == secretName {
		app.Spec.Source.PullSecretRef = ""
		if err := s.client.Update(r.Context(), &app); err != nil {
			writeError(w, err)
			return
		}
	}

	s.recordActivity(r, projectName, "delete", "app", appName,
		"Removed pull credentials for "+appName, "")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// upsertPullSecret creates or updates a dockerconfigjson Secret.
func (s *Server) upsertPullSecret(r *http.Request, namespace, name, appName string, dockerJSON []byte) error {
	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				constants.AppNameLabel:         appName,
				"app.kubernetes.io/managed-by": "mortise",
			},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			".dockerconfigjson": dockerJSON,
		},
	}

	var existing corev1.Secret
	err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, &existing)
	if errors.IsNotFound(err) {
		return s.client.Create(r.Context(), desired)
	}
	if err != nil {
		return err
	}

	existing.Data = desired.Data
	existing.Type = desired.Type
	existing.Labels = desired.Labels
	return s.client.Update(r.Context(), &existing)
}

// parsePullCredentials extracts registry and username from a dockerconfigjson
// Secret. The password is never returned.
func parsePullCredentials(secret *corev1.Secret) pullCredentialsResponse {
	raw, ok := secret.Data[".dockerconfigjson"]
	if !ok {
		return pullCredentialsResponse{}
	}

	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return pullCredentialsResponse{}
	}

	for registry, cred := range cfg.Auths {
		return pullCredentialsResponse{
			Registry:    registry,
			Username:    cred.Username,
			HasPassword: cred.Password != "",
		}
	}

	return pullCredentialsResponse{}
}
