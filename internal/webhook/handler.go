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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
	"github.com/MC-Meesh/mortise/internal/constants"
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
	getProject(ctx context.Context, name string) (*mortisev1alpha1.Project, error)
	listGitApps(ctx context.Context) ([]mortisev1alpha1.App, error)
	patchAppRevision(ctx context.Context, app *mortisev1alpha1.App, sha string) error
	listPreviewEnvironments(ctx context.Context, namespace string) ([]mortisev1alpha1.PreviewEnvironment, error)
	createPreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	updatePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
	deletePreviewEnvironment(ctx context.Context, pe *mortisev1alpha1.PreviewEnvironment) error
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

// dispatchPREvent handles pull_request events: creates, updates, or deletes
// PreviewEnvironment CRDs for matching Apps. Preview is gated at the Project
// level (SPEC §5.8): every App in a Project with preview.enabled=true
// participates in each open PR's preview namespace.
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

		switch pr.Action {
		case "opened", "synchronize":
			h.handlePROpenOrSync(ctx, app, project, preview, pr)
		case "closed":
			h.handlePRClosed(ctx, app, pr)
		default:
			log.Info("ignoring PR action", "action", pr.Action)
		}
		matched++
	}

	if matched == 0 {
		log.Info("no matching apps for PR event", "repo", pr.Repo, "number", pr.Number)
	}
}

func (h *Handler) handlePROpenOrSync(ctx context.Context, app *mortisev1alpha1.App, project *mortisev1alpha1.Project, preview *mortisev1alpha1.PreviewConfig, pr PREvent) {
	log := logf.FromContext(ctx)

	// Preview envs inherit settings from a source environment. Resolve which
	// env to clone from: prefer "staging" if it exists, otherwise the first
	// non-production env. If the project only has "production", warn and skip.
	sourceEnv := resolvePreviewSourceEnv(project)
	if sourceEnv == "" {
		log.Info("skipping preview env: project has no non-production environment to inherit from; add a staging or development environment",
			"project", project.Name, "app", app.Name, "pr", pr.Number)
		return
	}

	peName := previewEnvName(app.Name, pr.Number)

	// Resolve domain from template.
	domain := ""
	if preview != nil {
		domain = resolvePreviewDomainTemplate(preview.Domain, app.Name, pr.Number)
	}

	// Default TTL: 72h.
	ttlDuration := 72 * time.Hour
	if preview != nil && preview.TTL != "" {
		if parsed, err := time.ParseDuration(preview.TTL); err == nil {
			ttlDuration = parsed
		} else {
			log.Error(err, "parse TTL, using default 72h", "ttl", preview.TTL)
		}
	}

	// Find the source env override on the App (may be nil — defaults are fine).
	sourceEnvOverride := findAppEnv(app, sourceEnv)

	// Check if PreviewEnvironment already exists.
	existing, err := h.k8s.listPreviewEnvironments(ctx, app.Namespace)
	if err != nil {
		log.Error(err, "list preview environments")
		return
	}

	for j := range existing {
		if existing[j].Name == peName {
			// Update SHA to trigger rebuild.
			existing[j].Spec.PullRequest.SHA = pr.SHA
			existing[j].Spec.PullRequest.Branch = pr.Branch
			if err := h.k8s.updatePreviewEnvironment(ctx, &existing[j]); err != nil {
				log.Error(err, "update preview environment", "name", peName)
			} else {
				log.Info("updated preview environment SHA", "name", peName, "sha", pr.SHA)
			}
			return
		}
	}

	// Create new PreviewEnvironment.
	pe := &mortisev1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      peName,
			Namespace: app.Namespace,
		},
		Spec: mortisev1alpha1.PreviewEnvironmentSpec{
			AppRef:    app.Name,
			SourceEnv: sourceEnv,
			PullRequest: mortisev1alpha1.PullRequestRef{
				Number: pr.Number,
				Branch: pr.Branch,
				SHA:    pr.SHA,
			},
			Domain: domain,
			TTL:    metav1.Duration{Duration: ttlDuration},
		},
	}

	// Inherit from source environment.
	if sourceEnvOverride != nil {
		pe.Spec.Replicas = sourceEnvOverride.Replicas
		pe.Spec.Resources = sourceEnvOverride.Resources
		pe.Spec.Env = sourceEnvOverride.Env
		pe.Spec.Bindings = sourceEnvOverride.Bindings
	}

	// Apply project-level preview overrides.
	if preview != nil {
		if preview.Resources.CPU != "" || preview.Resources.Memory != "" {
			pe.Spec.Resources = preview.Resources
		}
	}

	if err := h.k8s.createPreviewEnvironment(ctx, pe); err != nil {
		log.Error(err, "create preview environment", "name", peName)
	} else {
		log.Info("created preview environment", "name", peName, "pr", pr.Number)
	}
}

func (h *Handler) handlePRClosed(ctx context.Context, app *mortisev1alpha1.App, pr PREvent) {
	log := logf.FromContext(ctx)

	existing, err := h.k8s.listPreviewEnvironments(ctx, app.Namespace)
	if err != nil {
		log.Error(err, "list preview environments for cleanup")
		return
	}

	peName := previewEnvName(app.Name, pr.Number)
	for j := range existing {
		if existing[j].Name == peName {
			if err := h.k8s.deletePreviewEnvironment(ctx, &existing[j]); err != nil {
				log.Error(err, "delete preview environment", "name", peName)
			} else {
				log.Info("deleted preview environment", "name", peName)
			}
			return
		}
	}
}

// branchFromRef strips the "refs/heads/" prefix from a git ref string.
// "refs/heads/main" → "main". Non-branch refs (tags) are returned as-is.
func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// repoMatches returns true if the App's configured repo URL and the webhook
// event's repo identifier refer to the same repository.
func repoMatches(appRepo, eventRepo string) bool {
	if appRepo == "" || eventRepo == "" {
		return false
	}
	a := normalizeRepo(appRepo)
	b := normalizeRepo(eventRepo)
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a) {
		return true
	}
	return false
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
	case "opened":
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

// previewEnvName returns the name for a PreviewEnvironment CRD.
func previewEnvName(appName string, prNumber int) string {
	return fmt.Sprintf("%s-preview-pr-%d", appName, prNumber)
}

// resolvePreviewDomainTemplate replaces {number} and {app} in a domain template.
func resolvePreviewDomainTemplate(template, appName string, prNumber int) string {
	if template == "" {
		return ""
	}
	result := strings.ReplaceAll(template, "{number}", fmt.Sprintf("%d", prNumber))
	result = strings.ReplaceAll(result, "{app}", appName)
	return result
}

// findAppEnv returns the App's per-environment override for the named env,
// or nil when the App declares no override for that env. Callers that need
// defaults should treat nil as "use App defaults".
func findAppEnv(app *mortisev1alpha1.App, envName string) *mortisev1alpha1.Environment {
	for i := range app.Spec.Environments {
		if app.Spec.Environments[i].Name == envName {
			return &app.Spec.Environments[i]
		}
	}
	return nil
}

// resolvePreviewSourceEnv picks the project environment that preview envs
// should inherit from. It prefers "staging" if declared, otherwise falls back
// to the first non-production environment. Returns "" if no suitable env
// exists (i.e. the project only has "production").
func resolvePreviewSourceEnv(project *mortisev1alpha1.Project) string {
	if project == nil {
		return ""
	}
	var firstNonProd string
	for _, env := range project.Spec.Environments {
		if env.Name == "staging" {
			return "staging"
		}
		if env.Name != "production" && firstNonProd == "" {
			firstNonProd = env.Name
		}
	}
	return firstNonProd
}
