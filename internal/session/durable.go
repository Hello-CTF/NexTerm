package session

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type durableVersionFloor interface {
	DurableVersions() (eventVersion, gridRevision uint64, err error)
	PersistDurableVersions(eventVersion, gridRevision uint64) error
}

type durableGridSource interface {
	DurableGrid() (cols, rows uint32, ok bool)
}

// durableOffsetAttacher is an optional extension for durable providers: providers supporting resume by absolute recording
// offset implement it, so reconnects skip the full replay from byte 0 and the client-side dropping of the consumed prefix.
// Providers without it fall back to Attach + prefixSkipChannel.
type durableOffsetAttacher interface {
	AttachFrom(ctx context.Context, id string, offset uint64) (base.DurableAttachment, error)
}

type DurableTranscriptOffsetStore interface {
	DurableTranscriptCatchUpBytes(tabID string) int64
	PersistDurableTranscriptOffset(tabID string, offset int64)
	DeleteDurableTranscriptOffset(tabID string)
}

// DurableTabTitleStore persists custom tab titles of durable tabs so a
// recovered tab keeps the title its user gave it across restarts.
type DurableTabTitleStore interface {
	DurableTabTitle(tabID string) string
	PersistDurableTabTitle(tabID string, title string)
	DeleteDurableTabTitle(tabID string)
}

// Owner-scoped variants of the durable stores: methods take a context so the
// implementation can isolate rows by the session owner carried in it. The
// Context suffix keeps them additive — a store can implement both the legacy
// and the owner-scoped interface. Stores that implement them get owner
// isolation on multi-user servers; a legacy adapter on a non-open server must
// fail closed (zero-value reads, skipped writes) rather than run ownerless.
type OwnerScopedDurableTranscriptOffsetStore interface {
	DurableTranscriptCatchUpBytesContext(ctx context.Context, tabID string) int64
	PersistDurableTranscriptOffsetContext(ctx context.Context, tabID string, offset int64)
	DeleteDurableTranscriptOffsetContext(ctx context.Context, tabID string)
}

type OwnerScopedDurableTabTitleStore interface {
	DurableTabTitleContext(ctx context.Context, tabID string) string
	PersistDurableTabTitleContext(ctx context.Context, tabID, title string)
	DeleteDurableTabTitleContext(ctx context.Context, tabID string)
}

type durableTranscriptOffsetSource = DurableTranscriptOffsetStore
