package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// registerSyncCompareCommands registers the desktop account-sync row-by-row comparison entry: decisions happen on the Go side
// (the engine session DEK decrypts cloud objects, with the same LWW rules as the browser CompareConsole); desktop only.
func registerSyncCompareCommands(dispatcher *ipc.Dispatcher, syncService *syncservice.Service) error {
	if err := ipc.Register(dispatcher, syncservice.CommandCompare, func(ctx context.Context, _ *ipc.Call, _ struct{}) (syncservice.CompareResult, error) {
		return syncService.Compare(ctx)
	}); err != nil {
		return err
	}
	return ipc.RegisterNested(dispatcher, syncservice.CommandRepairUndecryptable, func(ctx context.Context, _ *ipc.Call, request syncservice.RepairUndecryptableRequest) (syncservice.RepairUndecryptableResult, error) {
		return syncService.RepairUndecryptable(ctx, request)
	})
}
