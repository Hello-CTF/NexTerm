package session

import (
	"context"
)

type durableVersionFloor interface {
	DurableVersions() (eventVersion, gridRevision uint64, err error)
	PersistDurableVersions(eventVersion, gridRevision uint64) error
}

type durableGridSource interface {
	DurableGrid() (cols, rows uint32, ok bool)
}

// Owner-scoped durable stores isolate rows by the session owner carried in
// the context. An empty owner keeps desktop and open-access rows ownerless.
type OwnerScopedDurableTranscriptOffsetStore interface {
	DurableTranscriptCatchUpBytesContext(ctx context.Context, tabID string) int64
	PersistDurableTranscriptOffsetContext(ctx context.Context, tabID string, offset int64)
	DeleteDurableTranscriptOffsetContext(ctx context.Context, tabID string)
}

type durableTranscriptOffsetSource = OwnerScopedDurableTranscriptOffsetStore
