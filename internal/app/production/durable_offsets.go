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

var _ session.OwnerScopedDurableTranscriptOffsetStore = durableTranscriptOffsets{}
