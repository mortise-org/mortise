package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/auth"
	"github.com/mortise-org/mortise/internal/authz"
	"github.com/mortise-org/mortise/internal/webhook"
)

// BuildLogProvider returns in-progress build logs for an App. Implemented by
// the build tracker store on the AppReconciler.
type BuildLogProvider interface {
	GetBuildLogs(key types.NamespacedName) []string
}

// Server is the REST API server that translates HTTP requests into CRD operations.
type Server struct {
	client        client.Client
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	restConfig    *rest.Config
	auth          auth.AuthProvider
	jwt           *auth.JWTHelper
	ui            fs.FS
	webhook       *webhook.Handler
	deviceFlow    *DeviceFlowHandler
	authz         authz.PolicyEngine
	buildLogs     BuildLogProvider
	proxies       *appProxyManager
}

// RESTConfig returns the rest.Config the server was built with. Exposed for
// tests that need to assert the config was plumbed through from construction.
func (s *Server) RESTConfig() *rest.Config {
	return s.restConfig
}

// SetBuildLogProvider sets the build log provider (called after reconciler setup).
func (s *Server) SetBuildLogProvider(p BuildLogProvider) {
	s.buildLogs = p
}

// NewServer creates a new API server.
// ui is an optional filesystem for serving the SvelteKit UI; pass nil to disable UI serving.
// restConfig is used for pod/exec streaming; pass nil in tests that don't exercise exec.
func NewServer(c client.Client, cs kubernetes.Interface, dc dynamic.Interface, restConfig *rest.Config, authProvider auth.AuthProvider, jwt *auth.JWTHelper, ui fs.FS, policy authz.PolicyEngine) *Server {
	kr := webhook.NewK8sReader(c)
	wh := webhook.New(kr)
	df := newDeviceFlowHandler(c)
	return &Server{
		client:        c,
		clientset:     cs,
		dynamicClient: dc,
		restConfig:    restConfig,
		auth:          authProvider,
		jwt:           jwt,
		ui:            ui,
		authz:         policy,
		webhook:       wh,
		deviceFlow:    df,
		proxies:       newAppProxyManager(),
	}
}

// authorize checks whether the authenticated principal is allowed to perform
// action on resource. Writes 401/403 and returns false on denial.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, resource authz.Resource, action authz.Action) bool {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{"authentication required"})
		return false
	}
	ok, err := s.authz.Authorize(r.Context(), *p, resource, action)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"authorization check failed"})
		return false
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, errorResponse{"forbidden"})
		return false
	}
	return true
}

// Handler returns the root HTTP handler with all routes mounted.
//
// URL scheme:
//
//	/api/auth/{status|setup|login}                                 unauthenticated
//	/api/webhooks/{provider}                                       unauthenticated — HMAC-verified
//	/api/auth/git/{provider}/device                                authenticated — device flow initiation
//	/api/auth/git/{provider}/device/poll                           authenticated — device flow polling
//	/api/auth/git/{provider}/status                                authenticated — connection status
//	/api/projects                                                  list/create
//	/api/projects/{project}                                        get/delete
//	/api/projects/{project}/apps                                   list/create
//	/api/projects/{project}/apps/{app}                             get/update/delete
//	/api/projects/{project}/apps/{app}/deploy                      deploy webhook
//	/api/projects/{project}/apps/{app}/rollback                   rollback to previous deploy
//	/api/projects/{project}/apps/{app}/promote                    promote image between envs
//	/api/projects/{project}/events                                 SSE project event stream
//	/api/projects/{project}/apps/{app}/logs                        SSE log stream
//	/api/projects/{project}/apps/{app}/pods                        list pod summaries
//	/api/projects/{project}/apps/{app}/secrets                     list/create
//	/api/projects/{project}/apps/{app}/secrets/{secretName}        delete
//	/api/projects/{project}/apps/{app}/tokens                     list/create deploy tokens
//	/api/projects/{project}/apps/{app}/tokens/{tokenName}         revoke deploy token
//	/api/projects/{project}/apps/{app}/env                        get/put/patch env vars
//	/api/projects/{project}/apps/{app}/env/import                 import .env file
//	/api/projects/{project}/stacks                                create stack from compose/template
//	/api/projects/{project}/apps/{app}/exec                       exec command in app pod
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// Unauthenticated auth endpoints.
	r.Get("/api/auth/status", s.Status)
	r.Post("/api/auth/setup", s.Setup)
	r.Post("/api/auth/login", s.Login)

	// Unauthenticated git forge webhook receiver (auth is via HMAC).
	// Webhook handler has its own 10MB limit via io.LimitReader; no global
	// body cap applied.
	r.Mount("/api/webhooks", s.webhook.Routes())

	// Authenticated /api routes.
	r.Route("/api", func(r chi.Router) {
		r.Use(maxBytesMiddleware(1 << 20)) // 1 MB body limit
		r.Group(func(r chi.Router) {
			r.Use(s.jwtAuthMiddleware)

			// Device flow: provider-parameterized routes for per-user git auth.
			r.Post("/auth/git/{provider}/device", s.deviceFlow.RequestCode)
			r.Post("/auth/git/{provider}/device/poll", s.deviceFlow.Poll)
			r.Get("/auth/git/{provider}/status", s.deviceFlow.GitTokenStatus)
			r.Post("/auth/git/{provider}/token", s.deviceFlow.StorePAT)

			// Admin user management
			r.Get("/admin/users", s.ListUsers)
			r.Post("/admin/users", s.CreateUser)
			r.Patch("/admin/users/{email}", s.UpdateUserRole)
			r.Delete("/admin/users/{email}", s.DeleteUser)

			r.Get("/gitproviders", s.ListGitProviders)
			r.Post("/gitproviders", s.CreateGitProvider)
			r.Delete("/gitproviders/{name}", s.DeleteGitProvider)
			r.Get("/gitproviders/{name}/webhook-secret", s.GetWebhookSecret)

			r.Post("/projects", s.CreateProject)
			r.Get("/projects", s.ListProjects)
			r.Get("/projects/{project}", s.GetProject)
			r.Delete("/projects/{project}", s.DeleteProject)

			// Project member management
			r.Get("/projects/{project}/members", s.ListMembers)
			r.Post("/projects/{project}/members", s.AddMember)
			r.Patch("/projects/{project}/members/{email}", s.UpdateMember)
			r.Delete("/projects/{project}/members/{email}", s.RemoveMember)

			r.Get("/projects/{project}/bindings", s.ListBindings)

			r.Get("/projects/{project}/environments", s.ListProjectEnvironments)
			r.Post("/projects/{project}/environments", s.CreateProjectEnvironment)
			r.Patch("/projects/{project}/environments/{name}", s.UpdateProjectEnvironment)
			r.Delete("/projects/{project}/environments/{name}", s.DeleteProjectEnvironment)

			r.Post("/projects/{project}/apps", s.CreateApp)
			r.Get("/projects/{project}/apps", s.ListApps)
			r.Get("/projects/{project}/apps/{app}", s.GetApp)
			r.Put("/projects/{project}/apps/{app}", s.UpdateApp)
			r.Delete("/projects/{project}/apps/{app}", s.DeleteApp)

			r.Post("/projects/{project}/stacks", s.CreateStack)
			r.Get("/templates", s.ListTemplates)

			r.Post("/projects/{project}/apps/{app}/exec", s.ExecInApp)
			r.Post("/projects/{project}/apps/{app}/rollback", s.Rollback)
			r.Post("/projects/{project}/apps/{app}/rebuild", s.Rebuild)
			r.Post("/projects/{project}/apps/{app}/redeploy", s.Redeploy)
			r.Post("/projects/{project}/apps/{app}/promote", s.Promote)
			r.Get("/projects/{project}/apps/{app}/build-logs", s.handleBuildLogs)
			r.Get("/projects/{project}/apps/{app}/pods", s.handleListPods)
			r.Post("/projects/{project}/apps/{app}/connect", s.handleConnect)
			r.Post("/projects/{project}/apps/{app}/disconnect", s.handleDisconnect)

			r.Post("/projects/{project}/apps/{app}/secrets", s.CreateSecret)
			r.Get("/projects/{project}/apps/{app}/secrets", s.ListSecrets)
			r.Delete("/projects/{project}/apps/{app}/secrets/{secretName}", s.DeleteSecret)

			r.Post("/projects/{project}/tokens", s.CreateProjectToken)
			r.Get("/projects/{project}/tokens", s.ListProjectTokens)
			r.Delete("/projects/{project}/tokens/{tokenName}", s.DeleteProjectToken)

			r.Post("/projects/{project}/apps/{app}/tokens", s.CreateToken)
			r.Get("/projects/{project}/apps/{app}/tokens", s.ListTokens)
			r.Delete("/projects/{project}/apps/{app}/tokens/{tokenName}", s.DeleteToken)

			r.Get("/projects/{project}/apps/{app}/env", s.GetEnv)
			r.Put("/projects/{project}/apps/{app}/env", s.PutEnv)
			r.Patch("/projects/{project}/apps/{app}/env", s.PatchEnv)
			r.Post("/projects/{project}/apps/{app}/env/import", s.ImportEnv)

			r.Get("/projects/{project}/shared-vars", s.GetSharedVars)
			r.Put("/projects/{project}/shared-vars", s.PutSharedVars)

			r.Get("/projects/{project}/apps/{app}/domains", s.ListDomains)
			r.Post("/projects/{project}/apps/{app}/domains", s.AddDomain)
			r.Delete("/projects/{project}/apps/{app}/domains/{domain}", s.RemoveDomain)

			r.Get("/repos", s.ListRepos)
			r.Get("/repos/{owner}/{repo}/branches", s.ListBranches)
			r.Get("/repos/{owner}/{repo}/tree", s.GetRepoTree)

			r.Get("/platform", s.GetPlatform)
			r.Patch("/platform", s.PatchPlatform)
		})

		// /deploy: accepts JWT OR deploy token (mrt_...) for CI systems.
		// The Deploy handler checks auth internally — if no JWT principal is
		// present it falls back to deploy token validation.
		r.Group(func(r chi.Router) {
			r.Use(s.optionalJWTMiddleware)
			r.Post("/projects/{project}/apps/{app}/deploy", s.Deploy)
		})

		// SSE endpoints: JWT may come via `?token=` query param as an EventSource
		// workaround. sseTokenQueryParamMiddleware runs before jwtAuthMiddleware
		// and promotes the query param onto the Authorization header.
		r.Group(func(r chi.Router) {
			r.Use(sseTokenQueryParamMiddleware)
			r.Use(s.jwtAuthMiddleware)
			r.Get("/projects/{project}/apps/{app}/logs", s.handleLogs)
			r.Get("/projects/{project}/events", s.handleProjectEvents)
		})
	})

	// UI: serve SvelteKit static files at all non-/api paths
	if s.ui != nil {
		uiHandler := http.FileServer(http.FS(s.ui))
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			// SPA fallback: if the requested file doesn't exist, serve index.html
			path := strings.TrimPrefix(req.URL.Path, "/")
			if path == "" {
				path = "index.html"
			}
			if _, err := fs.Stat(s.ui, path); err != nil {
				req.URL.Path = "/"
			}
			uiHandler.ServeHTTP(w, req)
		})
	}

	return r
}

// UIFS returns a sub-filesystem of the embed.FS rooted at the SvelteKit build directory.
// Pass the result to NewServer as the ui parameter.
func UIFS(embedded embed.FS, subPath string) (fs.FS, error) {
	return fs.Sub(embedded, subPath)
}
