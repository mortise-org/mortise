// Package webhook receives and verifies inbound git forge webhook payloads.
//
// The handler is mounted at /api/webhooks/{provider} (unauthenticated; auth is
// via HMAC). It verifies the payload signature using the secret stored in the
// GitProvider CRD's webhookSecretRef, parses push events, then patches the
// annotation mortise.dev/revision on every matching App so the App reconciler
// picks up the new commit SHA and triggers a rebuild.
package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/git"
)

// Handler handles inbound git forge webhooks.
type Handler struct {
	k8s k8sReader
}

// k8sReader is a minimal interface over the k8s client so Handler doesn't
// import controller-runtime directly in tests.
type k8sReader interface {
	getGitProvider(ctx context.Context, name string) (*mortisev1alpha1.GitProvider, error)
	getSecret(ctx context.Context, namespace, name, key string) (string, error)
	listGitApps(ctx context.Context) ([]mortisev1alpha1.App, error)
	patchAppRevision(ctx context.Context, app *mortisev1alpha1.App, sha string) error
}

// New creates a Handler.
func New(r k8sReader) *Handler {
	return &Handler{k8s: r}
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

	// Dispatch: find all Apps whose git repo + branch match, patch revision annotation.
	h.dispatchToApps(req.Context(), br)

	w.WriteHeader(http.StatusAccepted)
}

// dispatchToApps lists all git-source Apps and patches mortise.dev/revision on
// those whose repo URL and branch match the push event. Errors are logged but
// do not fail the HTTP response — the forge has already delivered the event.
func (h *Handler) dispatchToApps(ctx context.Context, br BuildRequest) {
	log := logf.FromContext(ctx)

	apps, err := h.k8s.listGitApps(ctx)
	if err != nil {
		log.Error(err, "list git apps for dispatch")
		return
	}

	pushedBranch := branchFromRef(br.Ref)

	if br.ChangedPaths == nil {
		log.Info("push payload has no commits[]; skipping watchPaths gate", "repo", br.Repo, "ref", br.Ref)
	}

	matched := 0
	for i := range apps {
		app := &apps[i]
		src := app.Spec.Source
		if src.Type != mortisev1alpha1.SourceTypeGit {
			continue
		}
		if !repoMatches(src.Repo, br.Repo) {
			continue
		}
		branch := src.Branch
		if branch == "" {
			branch = "main"
		}
		if branch != pushedBranch {
			continue
		}
		if !matchesWatchPaths(src.WatchPaths, br.ChangedPaths) {
			log.Info("skipping app: no changed paths match watchPaths", "app", app.Name, "namespace", app.Namespace, "watchPaths", src.WatchPaths)
			continue
		}
		if err := h.k8s.patchAppRevision(ctx, app, br.SHA); err != nil {
			log.Error(err, "patch app revision annotation", "app", app.Name, "namespace", app.Namespace)
			continue
		}
		log.Info("patched revision annotation", "app", app.Name, "namespace", app.Namespace, "sha", br.SHA)
		matched++
	}

	if matched == 0 {
		log.Info("no matching apps for push event", "repo", br.Repo, "ref", br.Ref)
	}
}

// branchFromRef strips the "refs/heads/" prefix from a git ref string.
// "refs/heads/main" → "main". Non-branch refs (tags) are returned as-is.
func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// repoMatches returns true if the App's configured repo URL and the webhook
// event's repo identifier refer to the same repository.
//
// The event may carry either "owner/repo" (short form from full_name) or a
// full HTTPS URL (from html_url). The App always stores a full URL.
//
// Normalization rules:
//   - Strip trailing ".git" from both sides.
//   - Lowercase everything.
//   - If both are full URLs, compare host+path.
//   - If one is a short path ("owner/repo"), check whether the other's URL path
//     ends with "/" + that short path (e.g. "github.com/org/repo" ends with "/org/repo").
func repoMatches(appRepo, eventRepo string) bool {
	if appRepo == "" || eventRepo == "" {
		return false
	}
	a := normalizeRepo(appRepo)
	b := normalizeRepo(eventRepo)
	if a == b {
		return true
	}
	// One may be a short path; check suffix containment.
	// Add a "/" prefix to avoid partial segment matches.
	if strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a) {
		return true
	}
	return false
}

// normalizeRepo returns a canonical lowercased string for comparison.
// Full URLs are reduced to "host/path" (no scheme, no leading slash on path).
// Short "owner/repo" style strings are returned lowercased.
func normalizeRepo(raw string) string {
	raw = strings.TrimSuffix(raw, ".git")

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err == nil {
			// e.g. "github.com/org/repo"
			return strings.ToLower(u.Host) + "/" + strings.ToLower(strings.TrimPrefix(u.Path, "/"))
		}
	}
	return strings.ToLower(raw)
}

// BuildRequest is the parsed push event payload.
type BuildRequest struct {
	Provider     string   // GitProvider name
	Repo         string   // full repo path (owner/repo or URL)
	Ref          string   // branch or tag ref
	SHA          string   // commit SHA
	ChangedPaths []string // deduped union of added/modified/removed paths across all commits; nil when the payload carries no commits[]
}

// pushPayload is the minimal common shape we extract from all three forges.
// All three forges (GitHub, GitLab, Gitea) use compatible commits[].{added,
// modified, removed} shapes, so one struct covers them all.
type pushPayload struct {
	Ref  string `json:"ref"`
	SHA  string `json:"after"`
	Repo struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
	Commits []struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
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

	// Collect a deduped union of added/modified/removed paths across all
	// commits. If commits[] is absent, leave ChangedPaths nil — the watchPaths
	// gate treats that as "unknown, don't filter".
	var changed []string
	if p.Commits != nil {
		seen := make(map[string]struct{})
		for _, c := range p.Commits {
			for _, group := range [][]string{c.Added, c.Modified, c.Removed} {
				for _, path := range group {
					if path == "" {
						continue
					}
					if _, ok := seen[path]; ok {
						continue
					}
					seen[path] = struct{}{}
					changed = append(changed, path)
				}
			}
		}
		if changed == nil {
			// Commits present but empty — still distinguishable from "no
			// commits key" so the gate can apply.
			changed = []string{}
		}
	}

	return BuildRequest{
		Repo:         repo,
		Ref:          p.Ref,
		SHA:          sha,
		ChangedPaths: changed,
	}, true
}

// matchesWatchPaths returns true when the push should trigger a rebuild for an
// App with the given watchPaths.
//
// Rules:
//   - Empty watchPaths → always true (no filter configured).
//   - Nil changedPaths → always true (payload had no commits[]; we can't
//     reason about what changed, so fall back to rebuild-on-any-push).
//   - Otherwise: any changed path that has any watchPath as a prefix → true.
//
// Leading slashes on watchPaths are stripped before comparison so users can
// write either "services/api" or "/services/api".
func matchesWatchPaths(watchPaths, changedPaths []string) bool {
	if len(watchPaths) == 0 {
		return true
	}
	if changedPaths == nil {
		return true
	}
	for _, wp := range watchPaths {
		wp = strings.TrimPrefix(wp, "/")
		if wp == "" {
			continue
		}
		for _, cp := range changedPaths {
			if strings.HasPrefix(cp, wp) {
				return true
			}
		}
	}
	return false
}
