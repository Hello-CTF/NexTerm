package session

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestAdaptEmitterPreservesSharedTopicAndPayload(t *testing.T) {
	var captured ipc.Event
	emitter := AdaptEmitter(ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		captured = event
		return nil
	}))
	payload := StatusEvent{SessionID: "s1", Status: StatusConnected}
	if err := emitter.EmitSessionEvent(context.Background(), Event{Topic: TopicSessionStatus, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if captured.Event != ipc.TopicSessionStatus {
		t.Fatalf("topic = %q", captured.Event)
	}
	if got, ok := captured.Payload.(StatusEvent); !ok || got != payload {
		t.Fatalf("payload = %#v", captured.Payload)
	}
}

func TestIPCErrorPreservesControlAndLifecycleCodes(t *testing.T) {
	tests := []struct {
		err  error
		code ipc.Code
	}{
		{err: ErrNotController, code: ipc.CodeNotController},
		{err: ErrSessionNotFound, code: ipc.CodeNotFound},
		{err: ErrTabNotFound, code: ipc.CodeNotFound},
		{err: ErrDisconnected, code: ipc.CodeDisconnected},
		{err: ErrUnsupported, code: ipc.CodeUnsupported},
		{err: ErrInvalidSize, code: ipc.CodeBadParam},
		{err: ErrHidden, code: ipc.CodeBadParam},
		{err: context.DeadlineExceeded, code: ipc.CodeTimeout},
		{err: errors.New("other"), code: ipc.CodeInternal},
	}
	for _, test := range tests {
		if got := IPCError(test.err).Code; got != test.code {
			t.Errorf("IPCError(%v) code = %s, want %s", test.err, got, test.code)
		}
	}
}
