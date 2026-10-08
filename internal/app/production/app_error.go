package production

import (
	"context"
	"log/slog"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func reportAppError(ctx context.Context, events ipc.Emitter, code, message string, cause error) {
	if cause != nil {
		slog.Warn(message, "code", code, "error", cause)
	} else {
		slog.Warn(message, "code", code)
	}
	_ = ipc.Emit(ctx, events, ipc.TopicAppError, ipc.AppErrorEvent{Code: code, Message: message})
}
