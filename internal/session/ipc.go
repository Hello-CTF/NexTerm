package session

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func AdaptEmitter(emitter ipc.Emitter) Emitter {
	return EmitterFunc(func(ctx context.Context, event Event) error {
		if emitter == nil {
			return ipc.ErrEventsUnavailable
		}
		return emitter.Emit(ctx, ipc.Event{Event: ipc.Topic(event.Topic), Payload: event.Payload})
	})
}

func IPCError(err error) *ipc.Error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr
	}
	switch {
	case errors.Is(err, ErrNotController):
		return ipc.WrapError(ipc.CodeNotController, ErrNotController.Error(), err)
	case errors.Is(err, ErrSessionNotFound), errors.Is(err, ErrTabNotFound):
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	case errors.Is(err, ErrDisconnected), errors.Is(err, base.ErrDisconnected):
		return ipc.WrapError(ipc.CodeDisconnected, err.Error(), err)
	case errors.Is(err, ErrUnsupported), errors.Is(err, base.ErrUnsupported):
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err)
	case errors.Is(err, ErrInvalidSize), errors.Is(err, hub.ErrInvalidChannel):
		return ipc.BadParam(err)
	case errors.Is(err, context.DeadlineExceeded):
		return ipc.WrapError(ipc.CodeTimeout, err.Error(), err)
	default:
		return ipc.NormalizeError(err)
	}
}
