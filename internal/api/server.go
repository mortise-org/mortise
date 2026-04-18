package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/MC-Meesh/mortise/internal/auth"
	"github.com/MC-Meesh/mortise/internal/webhook"
)

// BuildLogProvider returns in-progress build logs for an App. Implemented by
// the build tracker store on the AppReconciler.
type BuildLogProvider interface {
	GetBuildLogs(key types.NamespacedName) []string
}

// Server is the REST API server that translates HTTP requests into CRD operations.
type Server struct {
	client     client.Client
	clientset  kubernetes.Interface
	auth       auth.AuthProvider
	jwt        *auth.JWTHelper
	ui         fs.FS
	webhook    *webhook.Handler
	deviceFlow *DeviceFlowHandler
	buildLogs  BuildLogProvider
}

// SetBuildLogProvider sets the build log provider (called after reconciler setup).
func (s *Server) SetBuildLogProvider(p BuildLogProvider) {
	s.buildLogs = p
}

// NewServer creates a new API server.
// ui is an optional filesystem for serving the SvelteKit UI; pass nil to disable UI serving.
func NewServer(c client.Client, cs kubernetes.Interface, authProvider auth.AuthProvider, jwt *auth.JWTHelper, ui fs.FS) *Server {
	kr := webhook.NewK8sReader(c)
	wh := webhook.New(kr)
	df := newDeviceFlowHandler(c)
	return &Server{
		client:     c,
		clientset:  cs,
		auth:       authProvider,
		jwt:        jwt,
		ui:         ui,
		webhook:    wh,
		deviceFlow: df,
	}
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
//	/api/projects/{project}/apps/{app}/logs                        SSE log stream
//	/api/projects/{project}/apps/{app}/secrets                     list/create
//	/api/projects/{project}/apps/{app}/secrets/{secretName}        delete
//	/api/projects/{project}/apps/{app}/tokens                     list/create deploy tokens
//	/api/projects/{project}/apps/{app}/tokens/{tokenName}         revoke deploy token
//	/api/projects/{project}/apps/{app}/env                        get/put/patch env vars
//	/api/projects/{project}/apps/{app}/env/import                 import .env file
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

			r.Get("/gitproviders", s.ListGitProviders)
			r.Post("/gitproviders", s.CreateGitProvider)
			r.Delete("/gitproviders/{name}", s.DeleteGitProvider)
			r.Get("/gitproviders/{name}/webhook-secret", s.GetWebhookSecret)

			r.Post("/projects", s.CreateProject)
			r.Get("/projects", s.ListProjects)
			r.Get("/projects/{project}", s.GetProject)
			r.Delete("/projects/{project}", s.DeleteProject)

			r.Post("/projects/{project}/apps", s.CreateApp)
			r.Get("/projects/{project}/apps", s.ListApps)
			r.Get("/projects/{project}/apps/{app}", s.GetApp)
			r.Put("/projects/{project}/apps/{app}", s.UpdateApp)
			r.Delete("/projects/{project}/apps/{app}", s.DeleteApp)

			r.Post("/projects/{project}/apps/{app}/rollback", s.Rollback)
			r.Post("/projects/{project}/apps/{app}/promote", s.Promote)
			r.Get("/projects/{project}/apps/{app}/build-logs", s.handleBuildLogs)
			r.Get("/projects/{project}/apps/{app}/proxy-url", s.handleProxyURL)

			r.Post("/projects/{project}/apps/{app}/secrets", s.CreateSecret)
			r.Get("/projects/{project}/apps/{app}/secrets", s.ListSecrets)
			r.Delete("/projects/{project}/apps/{app}/secrets/{secretName}", s.DeleteSecret)

			r.Post("/projects/{project}/apps/{app}/tokens", s.CreateToken)
			r.Get("/projects/{project}/apps/{app}/tokens", s.ListTokens)
			r.Delete("/projects/{project}/apps/{app}/tokens/{tokenName}", s.DeleteToken)

			r.Get("/projects/{project}/apps/{app}/env", s.GetEnv)
			r.Put("/projects/{project}/apps/{app}/env", s.PutEnv)
			r.Patch("/projects/{project}/apps/{app}/env", s.PatchEnv)
			r.Post("/projects/{project}/apps/{app}/env/import", s.ImportEnv)

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

		// /logs: JWT may come via `?token=` query param as an EventSource
		// workaround. sseTokenQueryParamMiddleware runs before jwtAuthMiddleware
		// and promotes the query param onto the Authorization header.
		r.Group(func(r chi.Router) {
			r.Use(sseTokenQueryParamMiddleware)
			r.Use(s.jwtAuthMiddleware)
			r.Get("/projects/{project}/apps/{app}/logs", s.handleLogs)
		})
	})

	// App proxy: reverse-proxy to in-cluster app Services. No JWT required —
	// the app itself handles auth. Placed before the UI catch-all so /proxy/*
	// doesn't fall through to the SPA handler.
	r.HandleFunc("/proxy/{project}/{app}/*", s.handleProxy)
	r.HandleFunc("/proxy/{project}/{app}", s.handleProxy)

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
