package production

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
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
