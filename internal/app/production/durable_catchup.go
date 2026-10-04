package production

import (
	"context"
	"os"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type catchUpProvider struct {
	base.DurableProvider
	recordingPath func(id string) string
}

func (p *catchUpProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.DurableProvider.Attach(ctx, id)
	if attachment == nil {
		return nil, err
	}
	return &catchUpAttachment{DurableAttachment: attachment, boundary: p.catchUpBytes(id)}, nil
}

func (p *catchUpProvider) catchUpBytes(id string) int64 {
	if p.recordingPath == nil {
		return 0
	}
	info, err := os.Stat(p.recordingPath(id))
	if err != nil {
		return 0
	}
	return info.Size()
}

type catchUpAttachment struct {
	base.DurableAttachment
	boundary int64
}

func (a *catchUpAttachment) DurableCatchUpBytes() int64 { return a.boundary }

func (a *catchUpAttachment) DurableGrid() (uint32, uint32, bool) {
	if source, ok := a.DurableAttachment.(interface{ DurableGrid() (uint32, uint32, bool) }); ok {
		return source.DurableGrid()
	}
	return 0, 0, false
}

func (a *catchUpAttachment) DurableVersions() (uint64, uint64, error) {
	if source, ok := a.DurableAttachment.(interface {
		DurableVersions() (uint64, uint64, error)
	}); ok {
		return source.DurableVersions()
	}
	return 0, 0, nil
}

func (a *catchUpAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	if sink, ok := a.DurableAttachment.(interface {
		PersistDurableVersions(uint64, uint64) error
	}); ok {
		return sink.PersistDurableVersions(eventVersion, gridRevision)
	}
	return nil
}
