package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
	// ErrTerminalGone marks a handle whose tab no longer exists; the absence
	// was verified against the live tab set, so the terminal is gone rather
	// than merely unreachable.
	ErrTerminalGone = errors.New("句柄对应的终端标签页已不存在")
	// ErrTerminalReplaced marks a handle whose tab ID now belongs to a
	// different session: the exact identity the handle was issued for no
	// longer matches, so the handle must not be resolved to the new tab.
	ErrTerminalReplaced = errors.New("句柄对应的终端标签页已被替换（会话身份不一致）")
	// ErrUserInputPending marks a write refused because the terminal input
	// buffer changed after the job last observed it: new user input is
	// present and must be looked at (read_screen) or explicitly forced
	// through, never silently overwritten.
	ErrUserInputPending = errors.New("终端存在自上次查看以来的新输入（可能是用户输入）；请先 read_screen 查看，确认后再发送，或用 force=true 强制发送")
	// ErrNoLiveSession marks the resolver verdict that an asset has no live
	// session right now — the only condition under which terminal_create
	// may establish a new connection on explicit opt-in. Forbidden,
	// unsupported, and transient resolver failures never trigger connect.
	ErrNoLiveSession = errors.New("资产当前没有活动会话")
	// ErrAssetIdentityChanged marks a resolved asset identity that no
	// longer matches the session's bound identity at execution time: the
	// asset was replaced, reconfigured, or revoked between resolution and
	// execution, so the mutation fails closed instead of landing on the
	// wrong asset or attributing the old result to it.
	ErrAssetIdentityChanged = errors.New("资产身份在解析与执行之间已变化")
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

	AuthorizationID         string                             `json:"authorizationId,omitempty"`
	OriginID                string                             `json:"-"`
	ProductionConfirmations []execbatch.ProductionConfirmation `json:"-"`
	// AssetIdentities carries the immutable asset identities resolved before
	// execution for asset-targeted mutations: the outcome key binds to them,
	// and the tool re-validates them at execution time. Empty when identity
	// resolution is unavailable — the result then marks the operation as
	// identity-unverifiable instead of claiming a binding that does not exist.
	AssetIdentities []AssetIdentity `json:"-"`
	// ConnectedSessionID names the session this call's explicit connect=true
	// opt-in established before the outcome key was computed, so a later
	// create failure still names the residual session.
	ConnectedSessionID string `json:"-"`
	// BoundSessionID is the session the bound identities were resolved or
	// connected from, so the durable outcome record can re-verify that
	// exact session's identity on a later replay.
	BoundSessionID string `json:"-"`
}

type Output struct {
	OK                  bool                          `json:"ok"`
	Text                string                        `json:"text"`
	ModelText           string                        `json:"modelText,omitempty"`
	SubmittedSeq        *uint64                       `json:"submitted_seq,omitempty"`
	WriteConfirmed      bool                          `json:"write_confirmed,omitempty"`
	Notes               string                        `json:"notes,omitempty"`
	ProductionChallenge *ExecBatchProductionChallenge `json:"-"`
	ExitCode            int                           `json:"exitCode"`
	// ExitUnknown marks a completed action whose exit code could not be
	// determined (e.g. a waited-on command still running or a bare OSC 133;D
	// without a code). It must never be recorded or replayed as exit 0.
	ExitUnknown bool             `json:"exitUnknown,omitempty"`
	Truncated   bool             `json:"truncated"`
	Panic       bool             `json:"panic,omitempty"`
	ExecFailed  bool             `json:"-"`
	Change      *Change          `json:"change,omitempty"`
	Todos       []TodoItem       `json:"todos,omitempty"`
	Plan        string           `json:"plan,omitempty"`
	Question    *Question        `json:"question,omitempty"`
	Terminal    *CreatedTerminal `json:"terminal,omitempty"`
	// OperationID identifies an uncertain mutation (tab creation with asset
	// connection, paste submission) so a later call can reference the exact
	// operation when querying its result. It is deterministic per
	// job/call/tool and never implies exactly-once execution.
	OperationID string          `json:"operation_id,omitempty"`
	Wait        *WaitOutcome    `json:"wait,omitempty"`
	Terminals   []TerminalBrief `json:"terminals,omitempty"`
}

type ExecBatchProductionChallenge struct {
	Detail        execbatch.ConfirmationRequiredDetail
	Confirmations []execbatch.ProductionConfirmation
}

// CreatedTerminal describes a terminal tab created by terminal_create in
// foreground mode: the client opens and attaches it in the workspace.
type CreatedTerminal struct {
	TabID     string `json:"tabId"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title,omitempty"`
	Handle    string `json:"handle,omitempty"`
}

// TerminalBrief is one open terminal tab as listed by terminal_list.
type TerminalBrief struct {
	Handle    string `json:"handle"`
	TabID     string `json:"tab_id"`
	SessionID string `json:"session_id"`
	AssetID   string `json:"asset_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Title     string `json:"title,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Durable   bool   `json:"durable,omitempty"`
	Exited    bool   `json:"exited,omitempty"`
}

// TabDetail carries the per-tab identity and state surfaced by read_screen
// and context injection: kind, OSC title, shell-reported cwd, and the exact
// handle reference.
type TabDetail struct {
	TabID     string
	SessionID string
	Title     string
	CWD       string
	Kind      string
	Exited    bool
	Durable   bool
}

// WaitTabOutcome is the per-tab result metadata of a wait_for call. Done
// means the wait condition was satisfied; Idle means the tab went quiet
// without satisfying it; Running reflects the OSC 133 command state. FromSeq
// and ToSeq are the exact output anchors the wait scanned, so a follow-up
// since_seq read neither repeats nor skips output. Matched is the
// fully-satisfied verdict including quiet; ConditionMatched reports the
// pattern/lines condition alone when quiet is still pending.
type WaitTabOutcome struct {
	TabID            string `json:"tab_id"`
	Matched          bool   `json:"matched"`
	ConditionMatched bool   `json:"condition_matched,omitempty"`
	Running          bool   `json:"running,omitempty"`
	IdleMS           int64  `json:"idle_ms"`
	NewLines         int    `json:"new_lines"`
	FromSeq          uint64 `json:"from_seq"`
	ToSeq            uint64 `json:"to_seq"`
	Note             string `json:"note,omitempty"`
}

// WaitOutcome is the structured result of wait_for: which tabs satisfied the
// condition, whether the wait ended satisfied or timed out, and the mode it
// ran under.
type WaitOutcome struct {
	Mode      string           `json:"mode"`
	Satisfied bool             `json:"satisfied"`
	TimedOut  bool             `json:"timed_out"`
	Tabs      []WaitTabOutcome `json:"tabs"`
}

func OK(text string) Output {
	return Output{OK: true, Text: text}
}

func Fail(err error) Output {
	if err == nil {
		err = errors.New("操作失败（原因未记录），请重试")
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
	Text           string
	Tail           []string
	CursorRow      int
	CursorCol      int
	Cols           int
	Rows           int
	IdleMS         int64
	Seq            uint64
	AltScreen      bool
	BracketedPaste bool
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
	Tags     string
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

type WaitSnapshotTerminal interface {
	WaitSnapshot(context.Context, string) (Screen, error)
}

type PasteTerminal interface {
	WritePaste(context.Context, string, string) error
}

type TabSessionTerminal interface {
	TabSession(context.Context, string) (string, error)
}

type CreateTerminalOptions struct {
	Title   string
	Command string
}

type CreatedTab struct {
	TabID string
	Title string
}

// CreateTerminal is the optional Terminal extension opening a new terminal
// tab on a session. The session terminal implements it; terminals without tab
// management simply do not, and the tool stays disabled.
type CreateTerminal interface {
	Create(context.Context, string, CreateTerminalOptions) (CreatedTab, error)
}

// RenameTerminal is the optional Terminal extension renaming a terminal tab.
type RenameTerminal interface {
	Rename(context.Context, string, string) error
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

// ListTabsTerminal is the optional Terminal extension enumerating the open
// terminal tabs visible to the caller. Terminals without tab management do
// not implement it, and terminal_list stays disabled.
type ListTabsTerminal interface {
	ListTerminalTabs(context.Context) ([]TerminalBrief, error)
}

// TabDetailTerminal is the optional Terminal extension reporting per-tab
// identity and state (kind, OSC title, shell-reported cwd). Terminals
// without it simply omit the detail lines from read_screen output.
type TabDetailTerminal interface {
	TabDetail(context.Context, string) (TabDetail, error)
}

// TabQuietState is one tab's quiet reading at a point in the terminal's
// state: idle milliseconds and a version that bumps on any output or
// lifecycle change to the tab. Two group snapshots whose per-tab versions
// all match describe one consistent state.
type TabQuietState struct {
	TabID   string
	IdleMS  int64
	Version uint64
}

// QuietGroupSnapshotTerminal is the optional Terminal extension that reads
// the quiet state of a set of tabs in one consistent snapshot with per-tab
// versions. The close/error contract: the response must contain exactly
// one state per requested tab ID — every requested tab present exactly
// once, no extras — or a non-nil error; callers reject incomplete rosters
// and never treat them as a success. Terminals that cannot provide a
// consistent multi-tab read do not implement it; multi-tab quiet waits
// then degrade to a best-effort serial scan and say so in the result notes.
type QuietGroupSnapshotTerminal interface {
	QuietGroupSnapshot(ctx context.Context, tabIDs []string) ([]TabQuietState, error)
}

// PasteSubmitTerminal is the optional Terminal extension that writes a paste
// block and, after the requested submit delay, the Enter key under one and
// the same terminal write lock, so user input can never interleave between
// the paste and the submission. Implementations must also enforce the
// literal-newline guard inside that lock: when the in-lock bracketed-paste
// mode is off, content whose newlines would map to carriage returns is
// rejected before any byte is written. Terminals that cannot hold the lock
// across both writes do not implement it; send_keys then falls back to
// separate writes and says so explicitly in the result notes.
type PasteSubmitTerminal interface {
	WritePasteSubmit(ctx context.Context, tabID, text string, submit bool, delay time.Duration) error
}

// PasteLiteralTerminal is the optional Terminal extension that writes a
// paste under the terminal's write lock with the bracketed-paste mode read
// inside that same lock: when the mode is off, content whose newlines would
// map to carriage returns is rejected before any byte is written. The
// pre-write snapshot guard alone cannot close the on→off mode-flip window;
// terminals that do not implement this leave that window open, and send_keys
// marks it explicitly in the result notes.
type PasteLiteralTerminal interface {
	WritePasteLiteral(ctx context.Context, tabID, text string) error
}

// AssetIdentity is the immutable identity of an asset's configuration: its
// ID plus the config fingerprint the live session was connected with. A
// replaced or reconfigured asset yields a different identity, so binding an
// operation to it never attributes the old result to the new asset.
type AssetIdentity struct {
	AssetID           string
	ConfigFingerprint string
}

// SessionAssetIdentityResolver reports the immutable asset identity a
// session is currently bound to, so mutation tools can re-validate the
// resolved identity at execution time.
type SessionAssetIdentityResolver interface {
	SessionAssetIdentity(ctx context.Context, sessionID string) (AssetIdentity, error)
}

// AssetConnector is the optional dependency that connects an asset and
// returns the live session ID. Connecting is an uncertain mutation: the
// implementation reports the session it created so failures after a
// successful connect can name the residual session instead of hiding it.
type AssetConnector interface {
	ConnectAsset(ctx context.Context, assetID string) (sessionID string, err error)
}

// OutcomeAttempt is one durable outcome-ledger attempt of an operation, as
// classified for pre-side-effect adjudication and for the operation_result
// query. State is "initiated" (in-progress), "succeeded", or "failed".
type OutcomeAttempt struct {
	OperationID       string
	Kind              string
	State             string
	FailureStage      string
	ResidualSessionID string
	ResidualResource  string
	Result            json.RawMessage
	Error             string
}

// OutcomeAdjudication classifies what the durable, owner-scoped outcome
// ledger already holds under one operation prefix: the newest unfinished
// attempt, the newest finished attempt, and whether the family is conflicted
// (more than one unfinished attempt). A conflicted or unfinished family
// never reconnects blindly; a finished one replays only after its binding
// and original session identity verify.
type OutcomeAdjudication struct {
	Pending  *OutcomeAttempt
	Terminal *OutcomeAttempt
	Conflict bool
}

// DurableOutcomeLedger is the optional durable, owner-scoped outcome ledger
// (M74's AgentOutcomeListByPrefix+Adjudicate) used to adjudicate a recorded
// operation before any new side effect such as a connect, and to power the
// operation_result query. The production adapter maps agent.OutcomeLedger;
// nil keeps the in-process adjudication only.
type DurableOutcomeLedger interface {
	AdjudicateOutcome(ctx context.Context, prefix string) (OutcomeAdjudication, error)
	BeginOutcome(ctx context.Context, operationID, kind string) error
	CompleteOutcome(ctx context.Context, operationID string, attempt OutcomeAttempt) error
	OutcomeResult(ctx context.Context, operationID string) (OutcomeAttempt, error)
}

type Database interface {
	Tables(context.Context, string, string) ([]string, error)
	Describe(context.Context, string, string, string) (db.TableDescribe, error)
	Query(context.Context, string, string, uint64, time.Duration) (db.QueryResult, error)
	RedisScan(context.Context, string, uint64, string, uint64) (db.RedisScanResult, error)
}

type ExecTargetSet struct {
	SessionIDs []string
	Production bool
}

type Dependencies struct {
	Transport           TransportResolver
	Terminal            Terminal
	TabSession          func(string) string
	ResolveAssetSession func(context.Context, string) (string, error)
	ResolveExecTargets  func(context.Context, []string, []string) (ExecTargetSet, error)
	// ResolveAssetSessionIdentity resolves an asset to its live session and
	// the immutable identity the session is bound to (asset ID plus config
	// fingerprint). Production wiring implements it with the asset row's
	// config fingerprint; when absent, mutation tools cannot bind or
	// re-validate the asset identity and mark the operation unverifiable.
	ResolveAssetSessionIdentity func(context.Context, string) (string, AssetIdentity, error)
	// ResolveExecTargetIdentities resolves batch targets to sessions and the
	// immutable identity each session is bound to.
	ResolveExecTargetIdentities func(context.Context, []string, []string) (ExecTargetSet, []AssetIdentity, error)
	ExecBatch                   func(context.Context, execbatch.Request) (execbatch.Batch, error)
	AssetConnector              AssetConnector
	Database                    Database
	ListAssets                  func(context.Context) ([]Asset, error)
	DockerPS                    func(context.Context, string) ([]Container, error)
	DockerLogs                  func(context.Context, string, string, int, string) (string, error)
	DockerExec                  func(context.Context, string, string, string) (ExecResult, error)
	DockerAct                   func(context.Context, string, string, string) error
	DockerActionAudited         bool
	LocalHistory                bool
	Audit                       func(context.Context, AuditEntry) error
	Reminders                   ReminderScheduler
	Notify                      func(context.Context, string) error
	Grants                      *guard.Grants

	Outcome *outcome.Ledger
	// DurableOutcomes is the optional durable, owner-scoped outcome ledger
	// used to adjudicate recorded operations before any new side effect and
	// to power the operation_result query. Nil keeps the in-process
	// adjudication only.
	DurableOutcomes DurableOutcomeLedger
	Now             func() time.Time
	PollInterval    time.Duration
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
