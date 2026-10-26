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

type OwnerScopedDurableTabTitleStore interface {
	DurableTabTitleContext(ctx context.Context, tabID string) string
	PersistDurableTabTitleContext(ctx context.Context, tabID, title string)
	DeleteDurableTabTitleContext(ctx context.Context, tabID string)
}

// DurableTabTitleTimeStore is the optional extension of a title store that
// also keeps each title's decision time (Unix ms), so a restart reports the
// real age of a custom title instead of an unknown one.
type DurableTabTitleTimeStore interface {
	DurableTabTitleChangedAtContext(ctx context.Context, tabID string) int64
	PersistDurableTabTitleAtContext(ctx context.Context, tabID, title string, changedAt int64)
}

type durableTranscriptOffsetSource = OwnerScopedDurableTranscriptOffsetStore
