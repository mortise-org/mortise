package build

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	def, opt, detectedPort, err := b.buildSolveOpt(ctx, req, ch)
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
	ch <- BuildEvent{Type: EventSuccess, Digest: digest, DetectedPort: detectedPort}
}

// buildSolveOpt decides between Dockerfile and Railpack, returning the
// appropriate Definition (nil for Dockerfile frontend), SolveOpt, and any
// detected container port from EXPOSE directives or Railpack image config.
func (b *BuildKitClient) buildSolveOpt(ctx context.Context, req BuildRequest, ch chan<- BuildEvent) (*llb.Definition, bkclient.SolveOpt, int32, error) {
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
		useDockerfile = b.resolveDockerfileContext(&req, dockerfileName, ch)
	}

	if useDockerfile {
		port := parseDockerfileExpose(filepath.Join(req.dockerfileDir(), dockerfileName))
		return nil, b.dockerfileSolveOpt(req), port, nil
	}

	return b.railpackSolveOpt(ctx, req, ch)
}

// resolveDockerfileContext picks the BuildKit context root for a Dockerfile
// build when source.path is set. It honors an explicit req.ContextMode
// override, otherwise falls back to auto-detection. Returns true if a
// Dockerfile was found and the build should use the Dockerfile frontend.
//
// Auto-detection flow:
//  1. Dockerfile lives in the subdir (self-contained, Railway-style)
//     → context = subdir, unless the Dockerfile's COPY/ADD sources reference
//     the subdir prefix, which signals the user wrote the Dockerfile
//     assuming repo-root context → fall back to repo-root context.
//  2. Dockerfile only at repo root (monorepo pattern) → context = repo root.
//  3. Nothing found → return false (Railpack path).
//
// Mutates req: SourceDir and/or DockerfileDir may be rewritten so the caller
// passes the right dirs to dockerfileSolveOpt.
func (b *BuildKitClient) resolveDockerfileContext(req *BuildRequest, dockerfileName string, ch chan<- BuildEvent) bool {
	rootDir := req.SourceDir
	subDir := req.dockerfileDir()
	subHasDockerfile := statFile(filepath.Join(subDir, dockerfileName))
	rootHasDockerfile := subDir != rootDir && statFile(filepath.Join(rootDir, dockerfileName))

	switch req.ContextMode {
	case ContextModeSubdir:
		// User forces the subdirectory as the context. Dockerfile must be
		// in the subdir — a root-only Dockerfile can't be applied to a
		// subdir context in a meaningful way, so fall through to Railpack.
		if subHasDockerfile {
			req.SourceDir = subDir
			return true
		}
		return false
	case ContextModeRoot:
		// User forces the repo root as the context. Dockerfile may live
		// at either location.
		if rootHasDockerfile {
			req.DockerfileDir = rootDir
			return true
		}
		if subHasDockerfile {
			return true // context = rootDir (unchanged), dockerfile dir = subDir
		}
		return false
	}

	// Auto mode.
	if subHasDockerfile {
		// Heuristic B: peek at the Dockerfile. If any COPY/ADD source
		// starts with the subdir prefix (e.g. `COPY reddit-reply/landing/…`),
		// the Dockerfile was written assuming repo-root context — fall back.
		if prefix := relSubdirPrefix(rootDir, subDir); prefix != "" && dockerfileNeedsRootContext(filepath.Join(subDir, dockerfileName), prefix) {
			ch <- BuildEvent{Type: EventLog, Line: fmt.Sprintf("[context] Dockerfile at %s references %s — using repo root as build context", dockerfileName, prefix)}
			req.DockerfileDir = subDir
			return true
		}
		req.SourceDir = subDir
		return true
	}
	if rootHasDockerfile {
		req.DockerfileDir = rootDir
		return true
	}
	return false
}

// statFile reports whether path refers to an existing regular file.
func statFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// relSubdirPrefix returns the subdir path relative to the repo root, in POSIX
// form with a trailing slash (e.g. "services/api/"). Returns "" when subdir
// equals the repo root or is outside it.
func relSubdirPrefix(root, subdir string) string {
	rel, err := filepath.Rel(root, subdir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel) + "/"
}

// dockerfileNeedsRootContext scans a Dockerfile for COPY/ADD sources that
// begin with the given subdir prefix, indicating the Dockerfile expects the
// repo root as the build context. Skips `COPY --from=...` (stage copies) and
// comments. Conservative: on read errors, returns false so we keep the
// existing self-contained default.
func dockerfileNeedsRootContext(path, subdirPrefix string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "COPY ") && !strings.HasPrefix(upper, "ADD ") {
			continue
		}
		// Drop the leading COPY/ADD keyword and any flags (--chown=, --from=).
		// A --from=<stage> copy reads from a build stage, not the local
		// context, so the source paths can't point at our subdir prefix.
		rest := line[len("COPY"):]
		if strings.HasPrefix(upper, "ADD ") {
			rest = line[len("ADD"):]
		}
		fields := strings.Fields(rest)
		fromStage := false
		var srcs []string
		for _, f := range fields {
			if strings.HasPrefix(f, "--") {
				if strings.HasPrefix(f, "--from=") {
					fromStage = true
				}
				continue
			}
			srcs = append(srcs, f)
		}
		if fromStage || len(srcs) < 2 {
			continue
		}
		// The final token is the destination; all others are sources.
		for _, src := range srcs[:len(srcs)-1] {
			clean := strings.TrimPrefix(src, "./")
			clean = strings.TrimPrefix(clean, "/")
			if strings.HasPrefix(clean, subdirPrefix) {
				return true
			}
		}
	}
	return false
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
		// dockerfile.v0 frontend. Context may be the repo root or a
		// subdirectory depending on ContextMode and the auto-detection
		// in resolveDockerfileContext. The dockerfile dir may differ
		// from the context dir when source.path is set.
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
// plan, converts it to a Definition, and returns the Definition, SolveOpt, and
// any detected container port from the image config.
func (b *BuildKitClient) railpackSolveOpt(ctx context.Context, req BuildRequest, ch chan<- BuildEvent) (*llb.Definition, bkclient.SolveOpt, int32, error) {
	// Point Railpack at the source directory (the subdirectory, not repo root,
	// so it detects the right framework).
	appDir := req.dockerfileDir()
	rpApp, err := rpapp.NewApp(appDir)
	if err != nil {
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("railpack: init app: %w", err)
	}

	env := &rpapp.Environment{}
	result := rpcore.GenerateBuildPlan(rpApp, env, &rpcore.GenerateBuildPlanOptions{})
	if !result.Success || result.Plan == nil {
		msg := "railpack: failed to generate build plan"
		if len(result.Logs) > 0 {
			msg += ": " + result.Logs[len(result.Logs)-1].Msg
		}
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("%s", msg)
	}

	// Log detected providers.
	ch <- BuildEvent{Type: EventLog, Line: fmt.Sprintf("[railpack] detected: %v", result.DetectedProviders)}

	// Convert the build plan to LLB.
	platform, _ := rpbuildkit.ParsePlatformWithDefaults(b.cfg.DefaultPlatform)
	llbState, image, err := rpbuildkit.ConvertPlanToLLB(result.Plan, rpbuildkit.ConvertPlanOptions{
		BuildPlatform: platform,
	})
	if err != nil {
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("railpack: convert to LLB: %w", err)
	}

	// Marshal the LLB state to a Definition for Solve.
	def, err := llbState.Marshal(ctx)
	if err != nil {
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("railpack: marshal LLB: %w", err)
	}

	// Serialize image config (CMD, ENV, EXPOSE, etc.) so BuildKit
	// embeds it in the exported image.
	imageBytes, err := json.Marshal(image)
	if err != nil {
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("railpack: marshal image config: %w", err)
	}

	// Use LocalMounts (fsutil.FS) instead of LocalDirs — matches how
	// Railpack's own BuildWithBuildkitClient works.
	appFS, err := fsutil.NewFS(appDir)
	if err != nil {
		return nil, bkclient.SolveOpt{}, 0, fmt.Errorf("railpack: create FS: %w", err)
	}

	detectedPort := firstExposedPort(image.Config.ExposedPorts)

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
	}, detectedPort, nil
}

// firstExposedPort extracts the lowest numeric port from an OCI ExposedPorts
// map (keys like "3000/tcp"). Returns 0 when no valid port is found.
func firstExposedPort(ports map[string]struct{}) int32 {
	var best int32
	for k := range ports {
		raw := strings.SplitN(k, "/", 2)[0]
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n <= 0 || n > 65535 {
			continue
		}
		p := int32(n)
		if best == 0 || p < best {
			best = p
		}
	}
	return best
}

// parseDockerfileExpose reads a Dockerfile and returns the first EXPOSE port.
// Returns 0 if the file can't be read or has no EXPOSE directives.
func parseDockerfileExpose(path string) int32 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "EXPOSE ") {
			continue
		}
		for _, tok := range strings.Fields(line)[1:] {
			raw := strings.SplitN(tok, "/", 2)[0]
			n, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || n <= 0 || n > 65535 {
				continue
			}
			return int32(n)
		}
	}
	return 0
}
