package build

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	exptypes "github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/tonistiigi/fsutil"

	rpbuildkit "github.com/railwayapp/railpack/buildkit"
	rpcore "github.com/railwayapp/railpack/core"
	rpapp "github.com/railwayapp/railpack/core/app"
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
	solver solver
	cfg    Config
}

// New creates a BuildKitClient connected to the given buildkitd address.
func New(cfg Config) (*BuildKitClient, error) {
	var opts []bkclient.ClientOpt
	if cfg.TLSCACert != "" {
		opts = append(opts, bkclient.WithServerConfig(cfg.ServerName, cfg.TLSCACert))
	}
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		opts = append(opts, bkclient.WithCredentials(cfg.TLSCert, cfg.TLSKey))
	}

	c, err := bkclient.New(context.Background(), cfg.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("build: connecting to buildkitd: %w", err)
	}
	return &BuildKitClient{solver: c, cfg: cfg}, nil
}

// newWithSolver is used by tests to inject a fake solver.
func newWithSolver(cfg Config, s solver) *BuildKitClient {
	return &BuildKitClient{solver: s, cfg: cfg}
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

	// Decide build strategy: Dockerfile if present, Railpack otherwise.
	def, opt, err := b.buildSolveOpt(ctx, req, ch)
	if err != nil {
		ch <- BuildEvent{Type: EventFailure, Error: err.Error()}
		<-logDone
		return
	}

	resp, err := b.solver.Solve(ctx, def, opt, statusCh)
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

// buildSolveOpt decides between Dockerfile and Railpack, returning the
// appropriate Definition (nil for Dockerfile frontend) and SolveOpt.
func (b *BuildKitClient) buildSolveOpt(ctx context.Context, req BuildRequest, ch chan<- BuildEvent) (*llb.Definition, bkclient.SolveOpt, error) {
	dockerfileName := req.Dockerfile
	if dockerfileName == "" {
		dockerfileName = "Dockerfile"
	}

	useDockerfile := false
	if req.Mode == BuildModeDockerfile {
		useDockerfile = true
	} else if req.Mode == BuildModeRailpack {
		useDockerfile = false
	} else {
		// Auto mode: check for Dockerfile in the subdirectory first,
		// then repo root. Prefer subdirectory (self-contained pattern,
		// e.g. Railway-style) over repo root (monorepo pattern).
		subDir := req.dockerfileDir()
		if _, err := os.Stat(filepath.Join(subDir, dockerfileName)); err == nil {
			useDockerfile = true
			// Self-contained Dockerfile: use subdirectory as both context
			// and dockerfile dir (e.g. backend/Dockerfile with COPY . .)
			req.SourceDir = subDir
		} else if subDir != req.SourceDir {
			// Check repo root for a monorepo Dockerfile.
			if _, err := os.Stat(filepath.Join(req.SourceDir, dockerfileName)); err == nil {
				useDockerfile = true
				req.DockerfileDir = req.SourceDir
			}
		}
	}

	if useDockerfile {
		return nil, b.dockerfileSolveOpt(req), nil
	}

	return b.railpackSolveOpt(ctx, req, ch)
}

// dockerfileSolveOpt builds a SolveOpt for Dockerfile-based builds.
func (b *BuildKitClient) dockerfileSolveOpt(req BuildRequest) bkclient.SolveOpt {
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
		// and push to the target registry.
		Session: []session.Attachable{
			authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{}),
		},
	}
}

// railpackSolveOpt detects the framework via Railpack, generates an LLB build
// plan, converts it to a Definition, and returns the Definition + SolveOpt.
func (b *BuildKitClient) railpackSolveOpt(ctx context.Context, req BuildRequest, ch chan<- BuildEvent) (*llb.Definition, bkclient.SolveOpt, error) {
	// Point Railpack at the source directory (the subdirectory, not repo root,
	// so it detects the right framework).
	appDir := req.dockerfileDir()
	rpApp, err := rpapp.NewApp(appDir)
	if err != nil {
		return nil, bkclient.SolveOpt{}, fmt.Errorf("railpack: init app: %w", err)
	}

	env := &rpapp.Environment{}
	result := rpcore.GenerateBuildPlan(rpApp, env, &rpcore.GenerateBuildPlanOptions{})
	if !result.Success || result.Plan == nil {
		msg := "railpack: failed to generate build plan"
		if len(result.Logs) > 0 {
			msg += ": " + result.Logs[len(result.Logs)-1].Msg
		}
		return nil, bkclient.SolveOpt{}, fmt.Errorf("%s", msg)
	}

	// Log detected providers.
	ch <- BuildEvent{Type: EventLog, Line: fmt.Sprintf("[railpack] detected: %v", result.DetectedProviders)}

	// Convert the build plan to LLB.
	platform, _ := rpbuildkit.ParsePlatformWithDefaults(b.cfg.DefaultPlatform)
	llbState, image, err := rpbuildkit.ConvertPlanToLLB(result.Plan, rpbuildkit.ConvertPlanOptions{
		BuildPlatform: platform,
	})
	if err != nil {
		return nil, bkclient.SolveOpt{}, fmt.Errorf("railpack: convert to LLB: %w", err)
	}

	// Marshal the LLB state to a Definition for Solve.
	def, err := llbState.Marshal(ctx)
	if err != nil {
		return nil, bkclient.SolveOpt{}, fmt.Errorf("railpack: marshal LLB: %w", err)
	}

	// Serialize image config (CMD, ENV, EXPOSE, etc.) so BuildKit
	// embeds it in the exported image.
	imageBytes, err := json.Marshal(image)
	if err != nil {
		return nil, bkclient.SolveOpt{}, fmt.Errorf("railpack: marshal image config: %w", err)
	}

	// Use LocalMounts (fsutil.FS) instead of LocalDirs — matches how
	// Railpack's own BuildWithBuildkitClient works.
	appFS, err := fsutil.NewFS(appDir)
	if err != nil {
		return nil, bkclient.SolveOpt{}, fmt.Errorf("railpack: create FS: %w", err)
	}

	return def, bkclient.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context": appFS,
		},
		Exports: []bkclient.ExportEntry{
			{
				Type: bkclient.ExporterImage,
				Attrs: map[string]string{
					"name":                  req.PushTarget,
					"push":                  "true",
					"containerimage.config": string(imageBytes),
				},
			},
		},
		Session: []session.Attachable{
			authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{}),
		},
	}, nil
}
