package production

import (
	"context"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newSessionFSWatchHub(sessions *session.Manager, events ipc.Emitter, logger *slog.Logger) *fsWatchHub {
	return newFSWatchHub(func(ctx context.Context, sessionID string) (base.FileSystem, base.Transport, error) {
		transport, err := sessions.TransportFor(productionSessionRequestContext(ctx), sessionID)
		if err != nil {
			return nil, nil, err
		}
		provider, ok := transport.(base.FileTransport)
		if !ok {
			return nil, transport, base.ErrUnsupported
		}
		filesystem, err := provider.FileSystem(ctx)
		return filesystem, transport, err
	}, func(ctx context.Context, topic ipc.Topic, payload any) {
		if events != nil {
			_ = ipc.Emit(ctx, events, topic, payload)
		}
	}, logger)
}

func (h *fsWatchHub) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "fs_watch", func(ctx context.Context, _ *ipc.Call, input fsWatchRequest) (fsWatchStatusDTO, error) {
		return h.watch(ctx, input)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "fs_unwatch", func(ctx context.Context, _ *ipc.Call, input fsUnwatchRequest) (struct{}, error) {
		h.unwatch(ctx, input)
		return struct{}{}, nil
	})
}

func (h *fsWatchHub) shutdown(context.Context) error {
	h.close()
	return nil
}
