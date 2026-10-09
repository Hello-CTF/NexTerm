package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// registerSyncApplyCommands 注册浏览器明文应用入口: 会话与 CSRF 由 /rpc 传输层把关,
// 离线共享工作区(无账号)保持匿名可用。
func registerSyncApplyCommands(dispatcher *ipc.Dispatcher, syncService *syncservice.Service) error {
	return ipc.RegisterNested(dispatcher, syncservice.CommandApplyObjects, func(ctx context.Context, _ *ipc.Call, input syncservice.ApplyObjectsRequest) (syncservice.ApplyObjectsResult, error) {
		return syncService.ApplyObjects(ctx, input)
	})
}
