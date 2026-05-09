// Package webhook receives and verifies inbound git forge webhook payloads.
//
// The handler is mounted at /api/webhooks/{provider} (unauthenticated; auth is
// via HMAC). It verifies the payload signature using the secret stored in the
// GitProvider CRD's webhookSecretRef, parses push and pull_request events, then
// patches the annotation mortise.dev/revision on every matching App (push), or
// creates/updates/deletes PreviewEnvironment CRDs (pull_request).
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

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
	"github.com/mortise-org/mortise/internal/constants"
	"github.com/mortise-org/mortise/internal/git"
	"github.com/mortise-org/mortise/internal/previewsync"
)

// Handler handles inbound git forge webhooks.
type Handler struct {
	k8s                k8sReader
	gitAPIFromProvider func(*mortisev1alpha1.GitProvider, string, string) (git.GitAPI, error)
}

// k8sReader is a minimal interface over the k8s client so Handler doesn't
// import controller-runtime directly in tests.
type k8sReader interface {
	getGitProvider(ctx context.Context, name string) (*mortisev1alpha1.GitProvider, error)
	getSecret(ctx context.Context, namespace, name, key string) (string, error)
	getProject(ctx context.Context, name string) (*mortisev1alpha1.Project, error)
	listGitApps(ctx context.Context) ([]mortisev1alpha1.App, error)
	patchAppRevision(ctx context.Context, app *mortisev1alpha1.App, sha string) error
	listPreviewEnvironments(ctx context.Context, namespace string) ([]mortisev1alpha1.PreviewEnvironment, error)
	createPreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	updatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	deletePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	resolveGitTokenForApp(ctx context.Context, providerName, controlNamespace, createdBy, cachedOwner string) (git.TokenResult, error)
}

// New creates a Handler.
func New(r k8sReader) *Handler {
	return &Handler{k8s: r, gitAPIFromProvider: git.NewGitAPIFromProvider}
}

// ServeHTTP dispatches to the chi-routed sub-router.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{provider}", h.handleWebhook)
	return r
}

// handleWebhook receives a git forge webhook payload.
//
// @Summary Receive git forge webhook
// @Description HMAC-verified webhook receiver for GitHub, GitLab, and Gitea. Handles push events (triggers app redeploy) and pull_request events (creates/updates/deletes PreviewEnvironments). Authentication is via HMAC signature on the payload — no bearer token required.
// @Tags webhooks
// @Accept json
// @Produce plain
// @Param provider path string true "Git provider name (matches GitProvider CRD name)"
// @Success 202 {string} string "Accepted"
// @Failure 400 {string} string "Bad request or signature verification failed"
// @Failure 404 {string} string "Provider not found"
// @Router /webhooks/{provider} [post]
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

	var webhookSecret string
	if gp.Spec.WebhookSecretRef != nil {
		webhookSecret, err = h.k8s.getSecret(req.Context(),
			gp.Spec.WebhookSecretRef.Namespace,
			gp.Spec.WebhookSecretRef.Name,
			gp.Spec.WebhookSecretRef.Key)
		if err != nil {
			log.Error(err, "get webhook secret")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "webhook secret not configured", http.StatusForbidden)
		return
	}

	// Construct an ephemeral GitAPI for signature verification only.
	api, err := h.gitAPIFromProvider(gp, "" /* token unused */, webhookSecret)
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

	// Try parsing as a PR event first (PR payloads can also contain ref/after
	// fields that parsePushEvent would match).
	pr, ok := parsePREvent(gp.Spec.Type, body, req.Header)
	if ok {
		pr.Provider = providerName
		h.dispatchPREvent(req.Context(), pr)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Try parsing as a push event.
	br, ok := parsePushEvent(gp.Spec.Type, body)
	if ok {
		br.Provider = providerName
		h.dispatchToApps(req.Context(), br)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Not a push or PR event (e.g. ping); acknowledge silently.
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

// dispatchPREvent handles pull_request events by reconciling previews against
// the forge's current open-PR list for each matching App.
func (h *Handler) dispatchPREvent(ctx context.Context, pr PREvent) {
	log := logf.FromContext(ctx)

	apps, err := h.k8s.listGitApps(ctx)
	if err != nil {
		log.Error(err, "list git apps for PR dispatch")
		return
	}

	// Cache the parent Project once per PR — preview gating, staging lookup,
	// and env-set validation all hang off the same record.
	projectCache := make(map[string]*mortisev1alpha1.Project)
	projectKnown := make(map[string]bool)
	openPRCache := make(map[string][]git.PullRequestSnapshot)

	matched := 0
	for i := range apps {
		app := &apps[i]
		src := app.Spec.Source
		if src.Type != mortisev1alpha1.SourceTypeGit {
			continue
		}
		if !repoMatches(src.Repo, pr.Repo) {
			continue
		}

		projectName, ok := constants.ProjectFromControlNs(app.Namespace)
		if !ok {
			log.Info("skipping app not in control namespace", "app", app.Name, "namespace", app.Namespace)
			continue
		}

		var project *mortisev1alpha1.Project
		if projectKnown[projectName] {
			project = projectCache[projectName]
		} else {
			fetched, err := h.k8s.getProject(ctx, projectName)
			projectKnown[projectName] = true
			if err != nil {
				log.Error(err, "get Project for PR dispatch", "project", projectName)
				projectCache[projectName] = nil
				continue
			}
			projectCache[projectName] = fetched
			project = fetched
		}
		if project == nil {
			continue
		}
		preview := project.Spec.Preview
		if preview == nil || !preview.Enabled {
			continue
		}

		cacheKey := gpRepoKey(pr.Provider, app.Namespace, src.Repo)
		openPRs, ok := openPRCache[cacheKey]
		if !ok {
			gp, err := h.k8s.getGitProvider(ctx, app.Spec.Source.ProviderRef)
			if err != nil {
				log.Error(err, "get GitProvider for preview sync", "provider", app.Spec.Source.ProviderRef)
				continue
			}
			createdBy := app.Annotations["mortise.dev/created-by"]
			cachedOwner := app.Annotations["mortise.dev/git-token-owner"]
			tokenResult, err := h.k8s.resolveGitTokenForApp(ctx, gp.Name, app.Namespace, createdBy, cachedOwner)
			if err != nil {
				log.Error(err, "resolve git token for preview sync", "app", app.Name, "provider", gp.Name)
				continue
			}
			api, err := h.gitAPIFromProvider(gp, tokenResult.Token, "")
			if err != nil {
				log.Error(err, "create git API for preview sync", "app", app.Name)
				continue
			}
			openPRs, err = api.ListOpenPullRequests(ctx, app.Spec.Source.Repo)
			if err != nil {
				log.Error(err, "list open pull requests for preview sync", "app", app.Name, "repo", app.Spec.Source.Repo)
				continue
			}
			openPRCache[cacheKey] = openPRs
		}
		if pr.Action == "closed" {
			openPRs = filterClosedPR(openPRs, pr.Number)
		}
		if err := previewsync.ReconcileAppPreviews(ctx, previewStore{reader: h.k8s}, app, project, preview, openPRs); err != nil {
			log.Error(err, "reconcile previews from webhook", "app", app.Name, "project", project.Name)
			continue
		}
		matched++
	}

	if matched == 0 {
		log.Info("no matching apps for PR event", "repo", pr.Repo, "number", pr.Number)
	}
}

func filterClosedPR(openPRs []git.PullRequestSnapshot, closedNumber int) []git.PullRequestSnapshot {
	if closedNumber == 0 || len(openPRs) == 0 {
		return openPRs
	}
	filtered := openPRs[:0]
	for _, pr := range openPRs {
		if pr.Number == closedNumber {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered
}

// branchFromRef strips the "refs/heads/" prefix from a git ref string.
// "refs/heads/main" → "main". Non-branch refs (tags) are returned as-is.
func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// repoMatches returns true if the App's configured repo URL and the webhook
// event's repo identifier refer to the same repository. Both sides must
// resolve to the same owner/repo pair after normalization.
func repoMatches(appRepo, eventRepo string) bool {
	if appRepo == "" || eventRepo == "" {
		return false
	}
	a := normalizeRepo(appRepo)
	b := normalizeRepo(eventRepo)
	return ownerRepo(a) == ownerRepo(b)
}

// ownerRepo extracts the "owner/repo" suffix from a normalized repo string,
// requiring both components to be present for a valid match.
func ownerRepo(normalized string) string {
	parts := strings.Split(normalized, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// normalizeRepo returns a canonical lowercased string for comparison.
func normalizeRepo(raw string) string {
	raw = strings.TrimSuffix(raw, ".git")

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err == nil {
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

// PREvent is the parsed pull_request event payload.
type PREvent struct {
	Provider string
	Repo     string
	Number   int
	Action   string // opened, synchronize, closed
	Branch   string // source branch
	SHA      string // head commit SHA
}

// pushPayload is the minimal common shape we extract from all three forges.
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

// prPayload is the minimal shape for pull_request events across forges.
type prPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	ObjectAttributes struct {
		Action       string `json:"action"`
		IID          int    `json:"iid"`
		SourceBranch string `json:"source_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		State string `json:"state"`
	} `json:"object_attributes"`
	Repo struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// parsePushEvent extracts a BuildRequest from a push payload.
// Returns false when the payload is not a push event or cannot be parsed.
func parsePushEvent(providerType mortisev1alpha1.GitProviderType, body []byte) (BuildRequest, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return BuildRequest{}, false
	}

	// If this looks like a PR event, don't parse as push.
	if _, hasPR := raw["pull_request"]; hasPR {
		return BuildRequest{}, false
	}

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

// parsePREvent extracts a PREvent from a pull_request / merge_request payload.
func parsePREvent(providerType mortisev1alpha1.GitProviderType, body []byte, header http.Header) (PREvent, bool) {
	switch providerType {
	case mortisev1alpha1.GitProviderTypeGitHub:
		return parseGitHubPREvent(body, header)
	case mortisev1alpha1.GitProviderTypeGitea:
		return parseGiteaPREvent(body, header)
	case mortisev1alpha1.GitProviderTypeGitLab:
		return parseGitLabPREvent(body, header)
	default:
		return PREvent{}, false
	}
}

func parseGitHubPREvent(body []byte, header http.Header) (PREvent, bool) {
	eventType := header.Get("X-GitHub-Event")
	if eventType != "pull_request" {
		return PREvent{}, false
	}

	var p prPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false
	}

	action := normalizeAction(p.Action)
	if action == "" {
		return PREvent{}, false
	}

	number := p.PullRequest.Number
	if number == 0 {
		number = p.Number
	}

	repo := p.Repo.FullName
	if repo == "" {
		repo = p.Repo.HTMLURL
	}

	return PREvent{
		Repo:   repo,
		Number: number,
		Action: action,
		Branch: p.PullRequest.Head.Ref,
		SHA:    p.PullRequest.Head.SHA,
	}, true
}

func parseGiteaPREvent(body []byte, header http.Header) (PREvent, bool) {
	eventType := header.Get("X-Gitea-Event")
	if eventType == "" {
		eventType = header.Get("X-GitHub-Event")
	}
	if eventType != "pull_request" {
		return PREvent{}, false
	}

	var p prPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false
	}

	action := normalizeAction(p.Action)
	if action == "" {
		return PREvent{}, false
	}

	number := p.PullRequest.Number
	if number == 0 {
		number = p.Number
	}

	repo := p.Repo.FullName
	if repo == "" {
		repo = p.Repo.HTMLURL
	}

	return PREvent{
		Repo:   repo,
		Number: number,
		Action: action,
		Branch: p.PullRequest.Head.Ref,
		SHA:    p.PullRequest.Head.SHA,
	}, true
}

func parseGitLabPREvent(body []byte, header http.Header) (PREvent, bool) {
	eventType := header.Get("X-Gitlab-Event")
	if eventType != "Merge Request Hook" {
		return PREvent{}, false
	}

	var p prPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PREvent{}, false
	}

	action := normalizeGitLabAction(p.ObjectAttributes.Action, p.ObjectAttributes.State)
	if action == "" {
		return PREvent{}, false
	}

	repo := p.Repo.FullName
	if repo == "" {
		repo = p.Repo.HTMLURL
	}

	return PREvent{
		Repo:   repo,
		Number: p.ObjectAttributes.IID,
		Action: action,
		Branch: p.ObjectAttributes.SourceBranch,
		SHA:    p.ObjectAttributes.LastCommit.ID,
	}, true
}

// normalizeAction maps forge-specific PR actions to our internal set.
func normalizeAction(action string) string {
	switch action {
	case "opened", "reopened":
		return "opened"
	case "synchronize", "synchronized":
		return "synchronize"
	case "closed", "merged":
		return "closed"
	default:
		return ""
	}
}

func normalizeGitLabAction(action, state string) string {
	switch action {
	case "open":
		return "opened"
	case "update":
		return "synchronize"
	case "close", "merge":
		return "closed"
	default:
		switch state {
		case "opened":
			return "opened"
		case "closed", "merged":
			return "closed"
		}
		return ""
	}
}

// matchesWatchPaths returns true when the push should trigger a rebuild for an
// App with the given watchPaths.
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

func gpRepoKey(providerName, namespace, repo string) string {
	return providerName + "|" + namespace + "|" + repo
}

type previewStore struct {
	reader k8sReader
}

func (p previewStore) ListPreviewEnvironments(ctx context.Context, namespace string) ([]mortisev1alpha1.PreviewEnvironment, error) {
	return p.reader.listPreviewEnvironments(ctx, namespace)
}

func (p previewStore) CreatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return p.reader.createPreviewEnvironment(ctx, pe)
}

func (p previewStore) UpdatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return p.reader.updatePreviewEnvironment(ctx, pe)
}

func (p previewStore) DeletePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error {
	return p.reader.deletePreviewEnvironment(ctx, pe)
}
