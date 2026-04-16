package build

import "context"

type BuildMode string

const (
	BuildModeAuto       BuildMode = "auto"
	BuildModeDockerfile BuildMode = "dockerfile"
	BuildModeRailpack   BuildMode = "railpack"
)

type BuildRequest struct {
	AppName    string
	Namespace  string
	SourceDir  string
	Mode       BuildMode
	Dockerfile string
	BuildArgs  map[string]string
	CacheFrom  string
	PushTarget string
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
