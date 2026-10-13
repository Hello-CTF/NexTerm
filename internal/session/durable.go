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

// durableOffsetAttacher 是 durable provider 的可选扩展: 支持按 recording
// 绝对偏移续传的 provider 实现它, 重连时免去从字节 0 全量重放再在客户端
// 丢弃已消费前缀。不实现的 provider 回退 Attach + prefixSkipChannel。
type durableOffsetAttacher interface {
	AttachFrom(ctx context.Context, id string, offset uint64) (base.DurableAttachment, error)
}

type DurableTranscriptOffsetStore interface {
	DurableTranscriptCatchUpBytes(tabID string) int64
	PersistDurableTranscriptOffset(tabID string, offset int64)
	DeleteDurableTranscriptOffset(tabID string)
}

type durableTranscriptOffsetSource = DurableTranscriptOffsetStore
