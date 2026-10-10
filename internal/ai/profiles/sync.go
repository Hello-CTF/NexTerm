package profiles

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// syncOptInReader 由 store 实现: 按账号用户读取 AI 模型档案同步 opt-in(默认开启)。
type syncOptInReader interface {
	AIProfileSyncOptIn(ctx context.Context, userID string) (bool, error)
}

// syncTombstoneRecorder 由 store 实现: 记录用户主动删除产生的同步墓碑。
type syncTombstoneRecorder interface {
	SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error
}

// aiProfileSyncDeleter 由 store 实现: 同一事务内写回档案状态并按 opt-in 记录删除墓碑(原子删除)。
type aiProfileSyncDeleter interface {
	AIProfilesDeleteTx(ctx context.Context, stateJSON string, tombstoneID string, deletedAt int64) error
}

// recordDeleteTombstone 在档案删除落盘后按删除者的 opt-in 补记用户删除墓碑(不带冲突三元组)。
// opt-in 按账号用户隔离且默认开启: 拿不到删除者身份(桌面直连/匿名模式)或 opt-in 未开启时不立碑,
// 从未同步或已禁用同步的档案删除不得删除远端副本。
func (m *Manager) recordDeleteTombstone(ctx context.Context, id string) error {
	userID, ok := ipc.UserIDFromContext(ctx)
	if !ok {
		return nil
	}
	reader, ok := m.settings.(syncOptInReader)
	if !ok {
		return nil
	}
	enabled, err := reader.AIProfileSyncOptIn(ctx, userID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	recorder, ok := m.settings.(syncTombstoneRecorder)
	if !ok {
		return nil
	}
	return recorder.SyncTombstoneRecord(ctx, id, store.SyncTombstoneKindAIProfile, ids.NowMS())
}
