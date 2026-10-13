package production

import (
	"context"

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
