package watches

import (
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// Status is the durable watch condition state machine: a watch is created
// armed, fires at most once per fire sequence into matched, and settles into
// cancelled, expired or unverifiable. matched is terminal unless the watch
// repeats, in which case acknowledging its event re-arms it from the current
// anchor.
type Status string

const (
	StatusArmed        Status = store.AgentWatchStatusArmed
	StatusMatched      Status = store.AgentWatchStatusMatched
	StatusCancelled    Status = store.AgentWatchStatusCancelled
	StatusExpired      Status = store.AgentWatchStatusExpired
	StatusUnverifiable Status = store.AgentWatchStatusUnverifiable
)

const (
	// EventKind is the envelope kind for every delivered watch event.
	EventKind = "watchEvent"
	// DefaultMaxExcerptBytes bounds the untrusted output excerpt copied into
	// an event; larger matches are truncated with a visible marker.
	DefaultMaxExcerptBytes = 4096
	// DefaultMaxCarryBytes bounds the durable partial-match overlap retained
	// per watch across chunk boundaries and restarts.
	DefaultMaxCarryBytes = 4096
)

var (
	ErrNotFound         = errors.New("watch 不存在")
	ErrConflict         = errors.New("watch 已被其他操作修改")
	ErrDetached         = errors.New("watch 目标未连接，输出不可验证")
	ErrIdentityMismatch = errors.New("watch 目标身份不匹配，输出不可验证")
	ErrInvalidCondition = errors.New("watch 条件无效")
	ErrEventCorrupt     = errors.New("watch 事件数据损坏")
)

// Condition is the persistent match condition evaluated against observed
// output after the watch anchor.
type Condition struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

const (
	ConditionContains = "contains"
	ConditionRegex    = "regex"
)

// Event is the watchEvent envelope. It is delivered to the agent runtime as
// untrusted tool data, never as a user message: Excerpt is attacker-influenced
// terminal output and must stay data. Delivery is at-least-once, so receivers
// must dedupe by DeliveryID; the same logical fire always carries the same
// DeliveryID and RunID across retries, redelivery and restarts.
type Event struct {
	Kind        string    `json:"kind"`
	DeliveryID  string    `json:"deliveryId"`
	RunID       string    `json:"runId"`
	WatchID     string    `json:"watchId"`
	TargetID    string    `json:"targetId"`
	Identity    string    `json:"identity"`
	Name        string    `json:"name,omitempty"`
	Condition   Condition `json:"condition"`
	MatchedFrom int64     `json:"matchedFrom"`
	MatchedTo   int64     `json:"matchedTo"`
	Excerpt     string    `json:"excerpt"`
	FiredAt     int64     `json:"firedAt"`
}

// ToolData is the delivery envelope for watch events: they travel as
// untrusted tool data, never as user messages. Channel is always "tool" and
// Untrusted is always true, so the agent runtime injects the event into the
// tool-result stream instead of the user-message stream.
type ToolData struct {
	Channel   string `json:"channel"`
	Untrusted bool   `json:"untrusted"`
	Event     Event  `json:"event"`
}

// NewToolData wraps a watch event into its untrusted tool-data envelope.
func NewToolData(event Event) ToolData {
	return ToolData{Channel: "tool", Untrusted: true, Event: event}
}

// Watch is the engine-level view of a persisted watch condition.
type Watch struct {
	ID          string
	OwnerID     string
	TargetID    string
	Identity    string
	Generation  uint64
	Name        string
	Condition   Condition
	Status      Status
	Repeat      bool
	AnchorSeq   int64
	Carry       []byte
	FireSeq     int64
	Revision    uint64
	DeliveryID  string
	RunID       string
	MatchedFrom int64
	MatchedTo   int64
	Excerpt     string
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ExpiresAt   time.Time
}

func watchFromRow(row model.AgentWatchRow) Watch {
	watch := Watch{
		ID: row.ID, OwnerID: row.OwnerID, TargetID: row.TargetID, Identity: row.Identity,
		Generation: row.Generation, Name: row.Name, Status: Status(row.Status), Repeat: row.Repeat,
		AnchorSeq: row.AnchorSeq, Carry: append([]byte(nil), row.CarryBlob...),
		FireSeq: row.FireSeq, Revision: row.Revision,
		DeliveryID: row.DeliveryID, RunID: row.RunID,
		MatchedFrom: row.MatchedFrom, MatchedTo: row.MatchedTo,
		Excerpt: row.Excerpt, LastError: row.LastError,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC(),
		UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(),
	}
	if row.ExpiresAt != nil {
		watch.ExpiresAt = time.UnixMilli(*row.ExpiresAt).UTC()
	}
	return watch
}

func optionalMillis(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	stamp := value.UnixMilli()
	return &stamp
}
