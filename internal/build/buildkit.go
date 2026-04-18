package build

import (
	"bufio"
	"bytes"
	"context"
	"fmt"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	exptypes "github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
)

// Config holds connection settings for a buildkitd daemon.
type Config struct {
	// Addr is the buildkitd address, e.g. "tcp://buildkitd:1234" or
	// "unix:///run/buildkit/buildkitd.sock".
	Addr string

	// TLSCACert is the path to the CA certificate for the buildkitd TLS
	// connection. Empty means no custom CA (system pool is used).
	TLSCACert string

	// TLSCert and TLSKey are the client certificate pair for mTLS. Both must
	// be non-empty for mTLS, or both empty for no client cert.
	TLSCert string
	TLSKey  string

	// ServerName is the TLS server name override. Only used when TLS is on.
	ServerName string

	// DefaultPlatform is the OCI platform string passed to the Dockerfile
	// frontend (e.g. "linux/amd64"). Empty lets BuildKit pick.
	DefaultPlatform string
}

// solver is the minimal surface of bkclient.Client used by BuildKitClient.
// Keeping it narrow lets unit tests inject a fake without spinning up a real
// BuildKit daemon.
type solver interface {
	Solve(ctx context.Context, def *llb.Definition, opt bkclient.SolveOpt, statusChan chan *bkclient.SolveStatus) (*bkclient.SolveResponse, error)
}

// BuildKitClient implements BuildClient using a buildkitd daemon.
type BuildKitClient struct {
	cfg    Config
	solver solver
}

// New creates a BuildKitClient that dials the buildkitd at cfg.Addr.
// The underlying gRPC connection is established lazily on the first Solve call.
func New(cfg Config) (*BuildKitClient, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("build: buildkitd addr must not be empty")
	}

	opts := []bkclient.ClientOpt{}
	if cfg.TLSCACert != "" {
		opts = append(opts, bkclient.WithServerConfig(cfg.ServerName, cfg.TLSCACert))
	}
	if cfg.TLSCert != "" {
		if cfg.TLSKey == "" {
			return nil, fmt.Errorf("build: TLSCert requires TLSKey")
		}
		opts = append(opts, bkclient.WithCredentials(cfg.TLSCert, cfg.TLSKey))
	}

	c, err := bkclient.New(context.Background(), cfg.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("build: connecting to buildkitd: %w", err)
	}

	return &BuildKitClient{cfg: cfg, solver: c}, nil
}

// newWithSolver creates a BuildKitClient with an injected solver. Used by tests.
func newWithSolver(cfg Config, s solver) *BuildKitClient {
	return &BuildKitClient{cfg: cfg, solver: s}
}

// Submit starts a build and returns a channel of BuildEvents. The channel is
// closed when the build completes (success or failure). Cancelling ctx cancels
// the underlying Solve call.
func (b *BuildKitClient) Submit(ctx context.Context, req BuildRequest) (<-chan BuildEvent, error) {
	if req.SourceDir == "" {
		return nil, fmt.Errorf("build: SourceDir must not be empty")
	}
	if req.PushTarget == "" {
		return nil, fmt.Errorf("build: PushTarget must not be empty")
	}

	events := make(chan BuildEvent, 64)
	go b.run(ctx, req, events)
	return events, nil
}

// run executes the build and writes events to ch, then closes it.
func (b *BuildKitClient) run(ctx context.Context, req BuildRequest, ch chan<- BuildEvent) {
	defer close(ch)

	statusCh := make(chan *bkclient.SolveStatus, 32)

	// Forward log lines from the status channel concurrently with Solve.
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		for st := range statusCh {
			for _, l := range st.Logs {
				scanner := bufio.NewScanner(bytes.NewReader(l.Data))
				for scanner.Scan() {
					select {
					case ch <- BuildEvent{Type: EventLog, Line: scanner.Text()}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	opt := b.solveOpt(req)
	resp, err := b.solver.Solve(ctx, nil, opt, statusCh)
	<-logDone // drain remaining log events before writing the terminal event

	if err != nil {
		ch <- BuildEvent{Type: EventFailure, Error: err.Error()}
		return
	}

	digest := ""
	if resp != nil {
		digest = resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	}
	ch <- BuildEvent{Type: EventSuccess, Digest: digest}
}

// solveOpt constructs the SolveOpt for the given build request.
func (b *BuildKitClient) solveOpt(req BuildRequest) bkclient.SolveOpt {
	frontendAttrs := map[string]string{}

	if req.Dockerfile != "" {
		frontendAttrs["filename"] = req.Dockerfile
	}
	for k, v := range req.BuildArgs {
		frontendAttrs["build-arg:"+k] = v
	}
	if b.cfg.DefaultPlatform != "" {
		frontendAttrs["platform"] = b.cfg.DefaultPlatform
	}

	var cacheImports []bkclient.CacheOptionsEntry
	if req.CacheFrom != "" {
		cacheImports = []bkclient.CacheOptionsEntry{
			{Type: "registry", Attrs: map[string]string{"ref": req.CacheFrom}},
		}
	}

	return bkclient.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: frontendAttrs,
		// LocalDirs maps the names "context" and "dockerfile" used by the
		// dockerfile.v0 frontend. Context is always the repo root so
		// Dockerfiles can COPY from sibling directories (monorepo pattern).
		// The dockerfile dir may be a subdirectory (source.path).
		LocalDirs: map[string]string{
			"context":    req.SourceDir,
			"dockerfile": req.dockerfileDir(),
		},
		Exports: []bkclient.ExportEntry{
			{
				Type: bkclient.ExporterImage,
				Attrs: map[string]string{
					"name": req.PushTarget,
					"push": "true",
				},
			},
		},
		CacheImports: cacheImports,
		// Use the host Docker credentials so BuildKit can pull base images
		// and push to the target registry. A follow-up can inject explicit
		// credentials when PlatformConfig wiring lands.
		Session: []session.Attachable{
			authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{}),
		},
	}
}

// Ensure BuildKitClient satisfies BuildClient at compile time.
var _ BuildClient = (*BuildKitClient)(nil)
