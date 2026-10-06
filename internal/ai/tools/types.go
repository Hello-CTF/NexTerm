package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
	"github.com/ProbiusOfficial/NexTerm/internal/terminal/shellintegr"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const (
	MaxOutputBytes = 120 << 10
	MaxOutputLines = 400
	MaxDiffBytes   = 1 << 20
	MaxPreviewSize = 512 << 10
)

var (
	ErrReadRequired         = errors.New("写入前必须先使用 read_file 成功读取目标；新文件需先用 read_file 确认不存在")
	ErrFileChanged          = errors.New("文件在读取或确认后已变化，请重新读取并生成差异")
	ErrInvalidToolCall      = errors.New("工具参数无效")
	ErrCrossScope           = errors.New("终端目标不属于当前 AI 会话范围")
	ErrTerminalInputChanged = errors.New("终端输入在确认后已变化，请重新评估")
	ErrQuestionRequired     = errors.New("需要用户回答")
	ErrDockerUnavailable    = errors.New("Docker 服务未配置")
)

type Scope struct {
	SessionID string `json:"sessionId,omitempty"`
	TabID     string `json:"tabId,omitempty"`
	ConnID    string `json:"connId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
}

type Schema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Call struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Reason string          `json:"reason,omitempty"`

	AuthorizationID string `json:"authorizationId,omitempty"`
}

type Output struct {
	OK       bool   `json:"ok"`
	Text     string `json:"text"`
	ExitCode int    `json:"exitCode"`
	// ExitUnknown marks a completed action whose exit code could not be
	// determined (e.g. a waited-on command still running or a bare OSC 133;D
	// without a code). It must never be recorded or replayed as exit 0.
	ExitUnknown bool       `json:"exitUnknown,omitempty"`
	Truncated   bool       `json:"truncated"`
	Panic       bool       `json:"panic,omitempty"`
	Change      *Change    `json:"change,omitempty"`
	Todos       []TodoItem `json:"todos,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	Question    *Question  `json:"question,omitempty"`
}

func OK(text string) Output {
	return Output{OK: true, Text: text}
}

func Fail(err error) Output {
	if err == nil {
		err = errors.New("未知错误")
	}
	return Output{Text: err.Error(), ExitCode: 1}
}

type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type Change struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type Preview struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
	Kind   string `json:"kind"`
}

type Screen struct {
	Text      string
	Tail      []string
	CursorRow int
	CursorCol int
	Cols      int
	Rows      int
	IdleMS    int64
	Seq       uint64
	AltScreen bool
}

type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
	Ports  string `json:"ports"`
}

type ExecResult struct {
	Output   string
	ExitCode int
}

type Asset struct {
	ID       string
	Name     string
	Kind     string
	Host     string
	Username string
}

type AuditEntry struct {
	SessionID  string
	AssetID    string
	Kind       string
	Payload    any
	ExitCode   int
	DurationMS int64
}

type TransportResolver func(context.Context, string) (base.Transport, error)

// CommandState is the OSC 133 command lifecycle snapshot of a terminal tab,
// as tracked by the session layer.
type CommandState = shellintegr.CommandState

type Terminal interface {
	Snapshot(context.Context, string) (Screen, error)
	Write(context.Context, string, []byte) error
}

// CommandStateTerminal is the optional Terminal extension exposing OSC 133
// command lifecycle state. The session terminal implements it; terminals
// without command tracking simply do not, and callers fall back to
// fire-and-forget semantics.
type CommandStateTerminal interface {
	CommandState(context.Context, string) (CommandState, error)
}

// OutputSinceTerminal is the optional Terminal extension exposing raw output
// bytes produced after a sequence anchor, plus the actual start and latest
// sequences. Terminals without a sequence-tracked ring do not implement it.
type OutputSinceTerminal interface {
	OutputSince(context.Context, string, uint64, int) ([]byte, uint64, uint64, error)
}

// AssetKindTerminal is the optional Terminal extension resolving the asset
// kind of a session on the server side, so privacy decisions never depend on
// client-supplied scope fields.
type AssetKindTerminal interface {
	SessionAssetKind(context.Context, string) (string, error)
}

type Database interface {
	Tables(context.Context, string, string) ([]string, error)
	Describe(context.Context, string, string, string) (db.TableDescribe, error)
	Query(context.Context, string, string, uint64, time.Duration) (db.QueryResult, error)
	RedisScan(context.Context, string, uint64, string, uint64) (db.RedisScanResult, error)
}

type Dependencies struct {
	Transport           TransportResolver
	Terminal            Terminal
	TabSession          func(string) string
	Database            Database
	ListAssets          func(context.Context) ([]Asset, error)
	DockerPS            func(context.Context, string) ([]Container, error)
	DockerLogs          func(context.Context, string, string, int, string) (string, error)
	DockerExec          func(context.Context, string, string, string) (ExecResult, error)
	DockerAct           func(context.Context, string, string, string) error
	DockerActionAudited bool
	Audit               func(context.Context, AuditEntry) error
	Reminders           ReminderScheduler

	Outcome      *outcome.Ledger
	Now          func() time.Time
	PollInterval time.Duration
}

type Preparation struct {
	Preview *Preview
	change  *preparedChange
}

type preparedChange struct {
	path         string
	key          string
	version      fileVersion
	before       fileState
	after        string
	replacements int
	create       bool
}

type fileState struct {
	known   bool
	missing bool
	content string
}

func (d Dependencies) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Dependencies) pollInterval() time.Duration {
	if d.PollInterval > 0 {
		return d.PollInterval
	}
	return 200 * time.Millisecond
}

func capText(text string) (string, bool) {
	truncated := false
	lines := strings.Split(text, "\n")
	if len(lines) > MaxOutputLines {
		lines = lines[:MaxOutputLines]
		truncated = true
	}
	text = strings.Join(lines, "\n")
	if len(text) > MaxOutputBytes {
		end := MaxOutputBytes
		for end > 0 && !utf8.ValidString(text[:end]) {
			end--
		}
		text = text[:end]
		truncated = true
	}
	if truncated {
		text += "\n[输出已截断]"
	}
	return text, truncated
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidToolCall, fmt.Sprintf(format, args...))
}
