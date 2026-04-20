package build

import "context"

type BuildMode string

const (
	BuildModeAuto       BuildMode = "auto"
	BuildModeDockerfile BuildMode = "dockerfile"
	BuildModeRailpack   BuildMode = "railpack"
)

// ContextMode selects the BuildKit context root when a subdirectory is in use.
// Empty = auto-detect (current smart default + heuristic scan of the
// Dockerfile). "root" pins the context to SourceDir. "subdir" pins it to
// DockerfileDir.
type ContextMode string

const (
	ContextModeRoot   ContextMode = "root"
	ContextModeSubdir ContextMode = "subdir"
)

type BuildRequest struct {
	AppName       string
	Namespace     string
	SourceDir     string // Repo-root build context.
	DockerfileDir string // Directory containing the Dockerfile. If empty, same as SourceDir.
	Mode          BuildMode
	ContextMode   ContextMode
	Dockerfile    string
	BuildArgs     map[string]string
	CacheFrom     string
	PushTarget    string
}

// dockerfileDir returns the directory to search for the Dockerfile. Falls back
// to SourceDir (repo root) when DockerfileDir is not set.
func (r BuildRequest) dockerfileDir() string {
	if r.DockerfileDir != "" {
		return r.DockerfileDir
	}
	return r.SourceDir
}

type EventType string

const (
	EventLog     EventType = "log"
	EventSuccess EventType = "success"
	EventFailure EventType = "failure"
)

type BuildEvent struct {
	Type   EventType
	Line   string
	Digest string // populated on success
	Error  string // populated on failure
}

type BuildClient interface {
	Submit(ctx context.Context, req BuildRequest) (<-chan BuildEvent, error)
}
