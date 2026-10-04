package production

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type catchUpProvider struct {
	base.DurableProvider
	database *store.Store
}

func (p *catchUpProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.Create(ctx, options)
	if attachment == nil {
		return nil, err
	}
	if err := p.database.DurableTranscriptOffsetDelete(ctx, options.ID); err != nil {
		return nil, err
	}
	return attachment, nil
}

func (p *catchUpProvider) DurableTranscriptCatchUpBytes(tabID string) int64 {
	if p.database == nil {
		return 0
	}
	offset, err := p.database.DurableTranscriptOffsetGet(context.Background(), tabID)
	if err != nil {
		return 0
	}
	return offset
}

func (p *catchUpProvider) PersistDurableTranscriptOffset(tabID string, offset int64) {
	if p.database == nil {
		return
	}
	_ = p.database.DurableTranscriptOffsetSet(context.Background(), tabID, offset)
}
