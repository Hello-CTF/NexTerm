package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type durableTranscriptOffsets struct {
	database *store.Store
}

// The session owner carried in the context isolates rows per account on
// multi-user servers; an empty owner keeps desktop and open-access rows
// ownerless.
func (o durableTranscriptOffsets) DurableTranscriptCatchUpBytesContext(ctx context.Context, tabID string) int64 {
	offset, err := o.database.DurableTranscriptOffsetGet(ctx, tabID)
	if err != nil {
		return 0
	}
	return offset
}

func (o durableTranscriptOffsets) PersistDurableTranscriptOffsetContext(ctx context.Context, tabID string, offset int64) {
	_ = o.database.DurableTranscriptOffsetSet(ctx, tabID, offset)
}

func (o durableTranscriptOffsets) DeleteDurableTranscriptOffsetContext(ctx context.Context, tabID string) {
	_ = o.database.DurableTranscriptOffsetDelete(ctx, tabID)
}

type durableTabTitles struct {
	database *store.Store
}

func (t durableTabTitles) DurableTabTitleContext(ctx context.Context, tabID string) string {
	title, err := t.database.DurableTabTitleGet(ctx, tabID)
	if err != nil {
		return ""
	}
	return title
}

func (t durableTabTitles) PersistDurableTabTitleContext(ctx context.Context, tabID, title string) {
	_ = t.database.DurableTabTitleSet(ctx, tabID, title)
}

func (t durableTabTitles) DurableTabTitleChangedAtContext(ctx context.Context, tabID string) int64 {
	changedAt, err := t.database.DurableTabTitleChangedAt(ctx, tabID)
	if err != nil {
		return 0
	}
	return changedAt
}

func (t durableTabTitles) PersistDurableTabTitleAtContext(ctx context.Context, tabID, title string, changedAt int64) {
	_ = t.database.DurableTabTitleSetAt(ctx, tabID, title, changedAt)
}

func (t durableTabTitles) DeleteDurableTabTitleContext(ctx context.Context, tabID string) {
	_ = t.database.DurableTabTitleDelete(ctx, tabID)
}

var (
	_ session.OwnerScopedDurableTranscriptOffsetStore = durableTranscriptOffsets{}
	_ session.OwnerScopedDurableTabTitleStore         = durableTabTitles{}
)
