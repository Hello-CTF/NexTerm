package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

var (
	ErrUnsupported   = errors.New("docker capability unsupported")
	ErrNoBackend     = errors.New("docker backend unavailable")
	ErrStreamClosed  = errors.New("docker stream closed")
	ErrInvalidAction = errors.New("invalid docker action")
)

type ContainerSummary struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Image          string  `json:"image"`
	State          string  `json:"state"`
	Status         string  `json:"status"`
	Ports          string  `json:"ports"`
	ComposeProject *string `json:"composeProject"`
}

type ImageSummary struct {
	ID           string `json:"id"`
	Repository   string `json:"repository"`
	Tag          string `json:"tag"`
	Size         string `json:"size"`
	CreatedSince string `json:"createdSince"`
}

type HostStats struct {
	ContainersRunning uint64 `json:"containersRunning"`
	ContainersTotal   uint64 `json:"containersTotal"`
	Images            uint64 `json:"images"`
}

type Overview struct {
	Containers []ContainerSummary `json:"containers"`
	HostStats  HostStats          `json:"hostStats"`
}

type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
	ActionPause   Action = "pause"
	ActionUnpause Action = "unpause"
	ActionKill    Action = "kill"
	ActionRemove  Action = "remove"
	ActionRename  Action = "rename"
)

type ActionOptions struct {
	Container   string
	Action      Action
	NewName     string
	Force       bool
	StopTimeout *int
}

type PullOptions struct {
	Reference    string
	RegistryAuth string
}

type LogsOptions struct {
	Container string
	Tail      int
	Follow    bool
	Since     string
}

type ExecOptions struct {
	Container string
	Cmd       []string
	TTY       bool
	Width     uint
	Height    uint
	Env       []string
	WorkDir   string
	User      string
}

type ExecResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
}

type LogStream struct {
	Reader io.ReadCloser
	TTY    bool
}

type ExecSession interface {
	io.ReadWriteCloser
	CloseWrite() error
	Resize(ctx context.Context, width, height uint) error
	Wait(ctx context.Context) (int, error)
	IsTTY() bool
}

type Backend interface {
	ListContainers(ctx context.Context, all bool) ([]container.Summary, error)
	InspectContainer(ctx context.Context, id string) (json.RawMessage, error)
	ContainerStats(ctx context.Context, id string) (container.StatsResponse, error)
	ListImages(ctx context.Context) ([]image.Summary, error)
	Action(ctx context.Context, options ActionOptions) error
	PullImage(ctx context.Context, options PullOptions) (string, error)
	RemoveImage(ctx context.Context, reference string, force bool) error
	OpenLogs(ctx context.Context, options LogsOptions) (LogStream, error)
	OpenExec(ctx context.Context, options ExecOptions) (ExecSession, error)
	ListDir(ctx context.Context, id, path string) ([]string, error)
	Close() error
}

type StatsSnapshotter interface {
	StatsSnapshot(ctx context.Context) (string, error)
}

type ContainerDTOLister interface {
	ListContainerDTOs(ctx context.Context, all bool) ([]ContainerSummary, error)
}

type ImageDTOLister interface {
	ListImageDTOs(ctx context.Context) ([]ImageSummary, error)
}

type BackendProvider interface {
	SDK(ctx context.Context, sessionID string) (Backend, error)
	Command(ctx context.Context, sessionID string) (Backend, error)
}

type StaticBackends struct {
	SDKBackend     Backend
	CommandBackend Backend
}

func (p StaticBackends) SDK(context.Context, string) (Backend, error) {
	if p.SDKBackend == nil {
		return nil, ErrNoBackend
	}
	return p.SDKBackend, nil
}

func (p StaticBackends) Command(context.Context, string) (Backend, error) {
	if p.CommandBackend == nil {
		return nil, ErrNoBackend
	}
	return p.CommandBackend, nil
}

type RegistryAuthProvider interface {
	RegistryAuth(ctx context.Context, sessionID, reference string) (string, error)
}

type RegistryAuthProviderFunc func(ctx context.Context, sessionID, reference string) (string, error)

func (f RegistryAuthProviderFunc) RegistryAuth(ctx context.Context, sessionID, reference string) (string, error) {
	return f(ctx, sessionID, reference)
}

type AuditEvent struct {
	SessionID string         `json:"sessionId"`
	AssetID   string         `json:"assetId,omitempty"`
	Source    string         `json:"source"`
	Kind      string         `json:"kind"`
	Payload   map[string]any `json:"payload"`
	ExitCode  int            `json:"exitCode"`
	Duration  time.Duration  `json:"duration"`
}

type Auditor interface {
	RecordDocker(ctx context.Context, event AuditEvent) error
}

type AuditorFunc func(ctx context.Context, event AuditEvent) error

func (f AuditorFunc) RecordDocker(ctx context.Context, event AuditEvent) error {
	return f(ctx, event)
}

type ActionRequest struct {
	SessionID string
	AssetID   string
	Source    string
	Options   ActionOptions
}

type ImagePullRequest struct {
	SessionID   string
	Reference   string
	UseCommand  bool
	AuthSession string
}

type LogsRequest struct {
	SessionID string
	Container string
	Tail      int
	Grep      string
}

type ExecRequest struct {
	SessionID string
	Container string
	Command   string
}

type FrameSink interface {
	Send(ctx context.Context, frame []byte) error
}

type FrameSinkFunc func(ctx context.Context, frame []byte) error

func (f FrameSinkFunc) Send(ctx context.Context, frame []byte) error {
	return f(ctx, frame)
}

type LogsAttachRequest struct {
	SessionID string
	Container string
	Tail      int
	Sink      FrameSink
}

type ExecAttachRequest struct {
	SessionID string
	Container string
	CustomCmd string
	Width     uint
	Height    uint
	Sink      FrameSink
}

type StreamExit struct {
	ExitCode int
	Err      error
}

type Config struct {
	ListTimeout      time.Duration
	ActionTimeout    time.Duration
	PullTimeout      time.Duration
	LogsTimeout      time.Duration
	StatsTimeout     time.Duration
	StatsConcurrency int
	Now              func() time.Time
}

func (c Config) withDefaults() Config {
	if c.ListTimeout <= 0 {
		c.ListTimeout = 30 * time.Second
	}
	if c.ActionTimeout <= 0 {
		c.ActionTimeout = 60 * time.Second
	}
	if c.PullTimeout <= 0 {
		c.PullTimeout = 10 * time.Minute
	}
	if c.LogsTimeout <= 0 {
		c.LogsTimeout = 60 * time.Second
	}
	if c.StatsTimeout <= 0 {
		c.StatsTimeout = 30 * time.Second
	}
	if c.StatsConcurrency <= 0 {
		c.StatsConcurrency = 4
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}
