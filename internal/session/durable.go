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

// Owner-scoped durable stores isolate rows by the session owner carried in
// the context. An empty owner keeps desktop and open-access rows ownerless.
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

type durableTranscriptOffsetSource = OwnerScopedDurableTranscriptOffsetStore
