package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type durableTranscriptOffsets struct {
	database *store.Store
}

func (o durableTranscriptOffsets) DurableTranscriptCatchUpBytes(tabID string) int64 {
	offset, err := o.database.DurableTranscriptOffsetGet(context.Background(), tabID)
	if err != nil {
		return 0
	}
	return offset
}

func (o durableTranscriptOffsets) PersistDurableTranscriptOffset(tabID string, offset int64) {
	_ = o.database.DurableTranscriptOffsetSet(context.Background(), tabID, offset)
}

func (o durableTranscriptOffsets) DeleteDurableTranscriptOffset(tabID string) {
	_ = o.database.DurableTranscriptOffsetDelete(context.Background(), tabID)
}

// Owner-scoped variants: the session owner carried in the context isolates
// rows per account on multi-user servers; an empty owner keeps the legacy
// shared-store behavior for desktop and open-access deployments.
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

func (t durableTabTitles) DurableTabTitle(tabID string) string {
	title, err := t.database.DurableTabTitleGet(context.Background(), tabID)
	if err != nil {
		return ""
	}
	return title
}

func (t durableTabTitles) PersistDurableTabTitle(tabID string, title string) {
	_ = t.database.DurableTabTitleSet(context.Background(), tabID, title)
}

func (t durableTabTitles) DeleteDurableTabTitle(tabID string) {
	_ = t.database.DurableTabTitleDelete(context.Background(), tabID)
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

func (t durableTabTitles) DeleteDurableTabTitleContext(ctx context.Context, tabID string) {
	_ = t.database.DurableTabTitleDelete(ctx, tabID)
}

var (
	_ session.DurableTranscriptOffsetStore            = durableTranscriptOffsets{}
	_ session.OwnerScopedDurableTranscriptOffsetStore = durableTranscriptOffsets{}
	_ session.DurableTabTitleStore                    = durableTabTitles{}
	_ session.OwnerScopedDurableTabTitleStore         = durableTabTitles{}
)
