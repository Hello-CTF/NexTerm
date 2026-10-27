package base

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrMetadataConflict = errors.New("durable terminal metadata changed")

type DurableCreateOptions struct {
	ID      string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
	// HistoryLimit bounds the recorded output the helper keeps for the
	// session; zero uses the helper default.
	HistoryLimit uint64
}

// DurableFreshTailBytes is how much recorded output a fresh open (app
// restart, another device, a new view without an offset) replays.
const DurableFreshTailBytes = 256 << 10

type DurableDescriptor struct {
	ID          string
	Incarnation string
	// Generation is the helper generation that listed the session.
	Generation       string
	CreatedAt        time.Time
	Title            string
	MetadataRevision uint64
	Cols             uint32
	Rows             uint32
	Dead             bool
	ExitCode         *int
	Signal           string
	FinishedAt       *time.Time
	// HistoryBase and HistoryNext bound the recorded output the helper still
	// keeps; HistoryTruncated reports that output before HistoryBase was
	// dropped by the history limit.
	HistoryBase      uint64
	HistoryNext      uint64
	HistoryTruncated bool
	// State is the helper's attention state for the session; nil when the
	// helper runs with state detection disabled.
	State *SessionState
	// Activity is the helper's in-memory activity record; nil when the
	// helper predates it.
	Activity *DurableActivity
}

// DurableActivity is when a durable session last produced output and when
// its last attached client left. A helper keeps it in memory only; after a
// helper restart a recovered session reports neither time.
type DurableActivity struct {
	// LastOutputAt is nil when the session has produced no output since the
	// helper started it.
	LastOutputAt *time.Time `json:"last_output_at,omitempty"`
	// DetachedAt is when the attached client count last dropped to zero, or
	// the creation time when no client ever detached; nil while Attached.
	DetachedAt *time.Time `json:"detached_at,omitempty"`
	// Attached reports that at least one client is attached now.
	Attached bool `json:"attached,omitempty"`
}

type DurableIdentity struct {
	CreatedAt   time.Time
	Incarnation string
	// Generation is the helper generation the session was last seen in.
	// When set, attach fails with durable.ErrGenerationMismatch after a
	// helper restart; an empty value skips that check.
	Generation string
}

type DurableRenameOptions struct {
	ID                       string
	Incarnation              string
	RequestID                string
	ExpectedMetadataRevision uint64
	Title                    string
}

type DurableRenameResult struct {
	Title            string
	MetadataRevision uint64
}

type DurableMetadataConflictError struct {
	CurrentTitle            string
	CurrentMetadataRevision uint64
}

func (e *DurableMetadataConflictError) Error() string {
	return fmt.Sprintf("%s: current title %q at revision %d", ErrMetadataConflict, e.CurrentTitle, e.CurrentMetadataRevision)
}

func (e *DurableMetadataConflictError) Unwrap() error {
	return ErrMetadataConflict
}

type DurableDescriptorLister interface {
	ListDurableDescriptors(context.Context) ([]DurableDescriptor, error)
}

type DurableMetadataProvider interface {
	DurableMetadataSupported(context.Context) (bool, error)
	RenameDurable(context.Context, DurableRenameOptions) (DurableRenameResult, error)
}

// DurableIdentityAttacher attaches only to the durable session whose identity
// matches; there is no unchecked attach path.
type DurableIdentityAttacher interface {
	AttachWithIdentity(context.Context, string, DurableIdentity) (DurableAttachment, error)
	AttachFromWithIdentity(context.Context, string, DurableIdentity, uint64) (DurableAttachment, error)
}

// DurableTailOptions selects a fresh-open replay.
type DurableTailOptions struct {
	// Tail is how much of the most recent output to display.
	Tail uint64
	// Retain, when set, is the earliest offset the caller still needs
	// streamed, such as a transcript catch-up point; bytes before the
	// displayed tail are streamed but should not be rendered.
	Retain *uint64
}

// DurableTailAttacher opens a durable session for a view that has no resume
// offset, replaying only the recent tail.
type DurableTailAttacher interface {
	AttachTailWithIdentity(context.Context, string, DurableIdentity, DurableTailOptions) (DurableAttachment, error)
}

// DurableReplay reports where a durable attachment started streaming.
type DurableReplay struct {
	HistoryBase uint64
	HistoryNext uint64
	// ReplayFrom is the offset of the first streamed byte.
	ReplayFrom uint64
	// DisplayFrom is where rendering starts; earlier streamed bytes were
	// requested through DurableTailOptions.Retain only.
	DisplayFrom uint64
	// Truncated reports that the requested offset was dropped by the history
	// limit and the stream resumed after the history base.
	Truncated bool
	// Prelude restores the screen buffer and input modes in effect at
	// DisplayFrom; it is rendered before the stream and occupies no offsets.
	Prelude []byte
}

// DurableReplayReporter is implemented by durable attachments that report
// their replay plan and the absolute offset of the next byte Read returns.
// The offset jumps forward when output was dropped by the history limit
// before it was streamed.
type DurableReplayReporter interface {
	DurableReplay() DurableReplay
	DurableOffset() uint64
}

// DurableHistoryRequest reads recorded output ending at Before.
type DurableHistoryRequest struct {
	Before   uint64
	MaxBytes int
}

// DurableHistoryChunk is recorded output in [Start, End).
type DurableHistoryChunk struct {
	Start       uint64
	End         uint64
	HistoryBase uint64
	HistoryNext uint64
	// Truncated reports that Start is the history base and earlier output
	// was dropped by the history limit.
	Truncated bool
	Data      []byte
}

// DurableHistoryReader reads recorded output without attaching.
type DurableHistoryReader interface {
	ReadDurableHistory(context.Context, string, DurableIdentity, DurableHistoryRequest) (DurableHistoryChunk, error)
}

type DurableIdentityKiller interface {
	KillWithIdentity(context.Context, string, DurableIdentity) error
}

type DurableProvider interface {
	Create(context.Context, DurableCreateOptions) (DurableAttachment, error)
	DurableIdentityAttacher
}

type DurableAttachment interface {
	Channel
	Kill(context.Context) error
}
