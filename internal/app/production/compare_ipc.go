package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// registerSyncCompareCommands 注册桌面端账号同步的逐条对比入口: 判定在 Go 侧完成
// (引擎会话 DEK 解云端对象, 与浏览器 CompareConsole 同一套 LWW 规则), 仅桌面端可用。
func registerSyncCompareCommands(dispatcher *ipc.Dispatcher, syncService *syncservice.Service) error {
	return ipc.Register(dispatcher, syncservice.CommandCompare, func(ctx context.Context, _ *ipc.Call, _ struct{}) (syncservice.CompareResult, error) {
		return syncService.Compare(ctx)
	})
}
