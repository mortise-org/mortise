package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

const defaultNamespace = "default"

// createAppRequest is the JSON body for creating an App.
type createAppRequest struct {
	Name      string                  `json:"name"`
	Namespace string                  `json:"namespace"`
	Spec      mortisev1alpha1.AppSpec `json:"spec"`
}

func (s *Server) CreateApp(w http.ResponseWriter, r *http.Request) {
	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"name is required"})
		return
	}
	if req.Namespace == "" {
		req.Namespace = defaultNamespace
	}

	app := &mortisev1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: req.Namespace,
		},
		Spec: req.Spec,
	}

	if err := s.client.Create(r.Context(), app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")

	var list mortisev1alpha1.AppList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	if err := s.client.List(r.Context(), &list, opts...); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, list.Items)
}

func (s *Server) GetApp(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = defaultNamespace
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, &app)
}

func (s *Server) UpdateApp(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = defaultNamespace
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	var spec mortisev1alpha1.AppSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid JSON: " + err.Error()})
		return
	}

	app.Spec = spec
	if err := s.client.Update(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, &app)
}

func (s *Server) DeleteApp(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = defaultNamespace
	}

	var app mortisev1alpha1.App
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &app); err != nil {
		writeError(w, err)
		return
	}

	if err := s.client.Delete(r.Context(), &app); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// writeError maps k8s API errors to HTTP status codes.
func writeError(w http.ResponseWriter, err error) {
	if errors.IsNotFound(err) {
		writeJSON(w, http.StatusNotFound, errorResponse{err.Error()})
		return
	}
	if errors.IsAlreadyExists(err) {
		writeJSON(w, http.StatusConflict, errorResponse{err.Error()})
		return
	}
	if errors.IsInvalid(err) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{err.Error()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
}
