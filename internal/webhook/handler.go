// Package webhook receives and verifies inbound git forge webhook payloads.
//
// The handler is mounted at /api/webhooks/{provider} (unauthenticated; auth is
// via HMAC). It verifies the payload signature using the secret stored in the
// GitProvider CRD's webhookSecretRef, parses push events, and writes build
// requests to an in-memory channel. Full wiring to the build stack is a
// follow-up.
package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/git"
)

// BuildRequest is the payload enqueued for a push event.
type BuildRequest struct {
	Provider string // GitProvider name
	Repo     string // full repo path (owner/repo or URL)
	Ref      string // branch or tag ref
	SHA      string // commit SHA
}

// Handler handles inbound git forge webhooks.
type Handler struct {
	k8s    k8sReader
	builds chan<- BuildRequest
}

// k8sReader is a minimal interface over the k8s client so Handler doesn't
// import controller-runtime directly in tests.
type k8sReader interface {
	getGitProvider(ctx context.Context, name string) (*mortisev1alpha1.GitProvider, error)
	getSecret(ctx context.Context, namespace, name, key string) (string, error)
}

// New creates a Handler. builds is the channel that receives parsed push events.
func New(r k8sReader, builds chan<- BuildRequest) *Handler {
	return &Handler{k8s: r, builds: builds}
}

// ServeHTTP dispatches to the chi-routed sub-router.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{provider}", h.handleWebhook)
	return r
}

func (h *Handler) handleWebhook(w http.ResponseWriter, req *http.Request) {
	log := logf.FromContext(req.Context())
	providerName := chi.URLParam(req, "provider")

	body, err := io.ReadAll(io.LimitReader(req.Body, 10<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	gp, err := h.k8s.getGitProvider(req.Context(), providerName)
	if err != nil {
		log.Error(err, "get GitProvider", "provider", providerName)
		http.Error(w, "provider not found", http.StatusNotFound)
		return
	}

	webhookSecret, err := h.k8s.getSecret(req.Context(),
		gp.Spec.WebhookSecretRef.Namespace,
		gp.Spec.WebhookSecretRef.Name,
		gp.Spec.WebhookSecretRef.Key)
	if err != nil {
		log.Error(err, "get webhook secret")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// We only need VerifyWebhookSignature, so construct an ephemeral GitAPI
	// without a real token — empty token is fine for signature-only use.
	api, err := git.NewGitAPIFromProvider(gp, "" /* token unused */, webhookSecret)
	if err != nil {
		log.Error(err, "build git api")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := api.VerifyWebhookSignature(body, req.Header); err != nil {
		log.Info("webhook signature invalid", "provider", providerName, "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	br, ok := parsePushEvent(gp.Spec.Type, body)
	if !ok {
		// Not a push event (e.g. ping); acknowledge silently.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	br.Provider = providerName

	select {
	case h.builds <- br:
		log.Info("enqueued build request", "provider", providerName, "repo", br.Repo, "ref", br.Ref, "sha", br.SHA)
	default:
		log.Info("build queue full, dropping", "provider", providerName, "repo", br.Repo)
	}

	w.WriteHeader(http.StatusAccepted)
}

// pushPayload is the minimal common shape we extract from all three forges.
type pushPayload struct {
	Ref  string `json:"ref"`
	SHA  string `json:"after"`
	Repo struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// parsePushEvent extracts a BuildRequest from a push payload.
// Returns false when the payload is not a push event or cannot be parsed.
func parsePushEvent(providerType mortisev1alpha1.GitProviderType, body []byte) (BuildRequest, bool) {
	// All three forges (GitHub, GitLab, Gitea) use compatible push payload shapes
	// for the fields we need (ref, after/checkout_sha, repository.full_name).
	// GitLab uses "checkout_sha" instead of "after"; handle both.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return BuildRequest{}, false
	}

	// GitLab uses checkout_sha; GitHub and Gitea use "after".
	shaKey := "after"
	if providerType == mortisev1alpha1.GitProviderTypeGitLab {
		shaKey = "checkout_sha"
	}

	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Ref == "" {
		return BuildRequest{}, false
	}
	sha := p.SHA
	if shaKey == "checkout_sha" {
		// Re-unmarshal from the raw map.
		if v, ok := raw[shaKey]; ok {
			_ = json.Unmarshal(v, &sha)
		}
	}
	if sha == "" || sha == "0000000000000000000000000000000000000000" {
		return BuildRequest{}, false
	}

	repo := p.Repo.FullName
	if repo == "" {
		repo = p.Repo.HTMLURL
	}

	return BuildRequest{
		Repo: repo,
		Ref:  p.Ref,
		SHA:  sha,
	}, true
}
