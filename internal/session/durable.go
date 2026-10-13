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

type durableTranscriptOffsetSource = DurableTranscriptOffsetStore
