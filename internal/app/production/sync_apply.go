package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// registerSyncApplyCommands registers the browser plaintext apply entry: session and CSRF are enforced by the /rpc transport layer,
// and offline shared workspaces (no account) stay anonymously usable.
func registerSyncApplyCommands(dispatcher *ipc.Dispatcher, syncService *syncservice.Service) error {
	return ipc.RegisterNested(dispatcher, syncservice.CommandApplyObjects, func(ctx context.Context, _ *ipc.Call, input syncservice.ApplyObjectsRequest) (syncservice.ApplyObjectsResult, error) {
		return syncService.ApplyObjects(ctx, input)
	})
}
