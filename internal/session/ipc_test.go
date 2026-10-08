package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
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
		{err: durable.ErrNotFound, code: ipc.CodeNotFound},
		{err: ErrDisconnected, code: ipc.CodeDisconnected},
		{err: ErrUnsupported, code: ipc.CodeUnsupported},
		{err: durable.ErrUnavailable, code: ipc.CodeUnsupported},
		{err: ErrInvalidSize, code: ipc.CodeBadParam},
		{err: ErrHidden, code: ipc.CodeBadParam},
		{err: durable.ErrInvalidInput, code: ipc.CodeBadParam},
		{err: durable.ErrAlreadyExists, code: ipc.CodeBadParam},
		{err: context.DeadlineExceeded, code: ipc.CodeTimeout},
		{err: errors.New("other"), code: ipc.CodeInternal},
	}
	for _, test := range tests {
		if got := IPCError(test.err).Code; got != test.code {
			t.Errorf("IPCError(%v) code = %s, want %s", test.err, got, test.code)
		}
	}
}

func TestIPCErrorUserMessagesAreActionable(t *testing.T) {
	tests := []struct {
		err  error
		code ipc.Code
		want string
	}{
		{err: ErrSessionNotFound, code: ipc.CodeNotFound, want: "会话不存在或已关闭"},
		{err: fmt.Errorf("lookup: %w", ErrSessionNotFound), code: ipc.CodeNotFound, want: "会话不存在或已关闭"},
		{err: ErrTabNotFound, code: ipc.CodeNotFound, want: "终端标签页不存在或已关闭"},
		{err: ErrNotController, code: ipc.CodeNotController, want: "请先获取控制权"},
		{err: ErrDisconnected, code: ipc.CodeDisconnected, want: "请重新连接"},
		{err: ErrSessionClosed, code: ipc.CodeDisconnected, want: "会话已关闭"},
		{err: ErrTabClosed, code: ipc.CodeDisconnected, want: "终端标签页已关闭"},
		{err: ErrUnsupported, code: ipc.CodeUnsupported, want: "不支持"},
		{err: ErrInvalidSize, code: ipc.CodeBadParam, want: "终端尺寸无效"},
		{err: context.DeadlineExceeded, code: ipc.CodeTimeout, want: "操作超时"},
		{err: fmt.Errorf("reconnect: %w", ErrAssetSessionConflict), code: ipc.CodeInternal, want: "该资产已有活动会话"},
	}
	for _, test := range tests {
		got := IPCError(test.err)
		if got.Code != test.code {
			t.Errorf("IPCError(%v) code = %s, want %s", test.err, got.Code, test.code)
		}
		if !strings.Contains(got.Message, test.want) {
			t.Errorf("IPCError(%v) message = %q, want it to contain %q", test.err, got.Message, test.want)
		}
	}
}

func TestIPCErrorHostKeyMessageIsActionable(t *testing.T) {
	keyErr := &ssh.HostKeyError{Pending: true, Presented: ssh.HostKey{Host: "203.0.113.10", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "SHA256:abc"}}
	got := IPCError(keyErr)
	if got.Code != ipc.CodeHostKeyPending {
		t.Fatalf("code = %s, want host_key_pending", got.Code)
	}
	for _, want := range []string{"确认", "SHA256:abc"} {
		if !strings.Contains(got.Message, want) {
			t.Fatalf("message = %q, want it to contain %q", got.Message, want)
		}
	}
}

func TestStatusEventErrorUsesUserMessage(t *testing.T) {
	connected := &Session{ID: "s1"}
	connectErr := &ssh.ConnectError{Kind: ssh.ErrorKindRefused, Op: "dial", Host: "203.0.113.10", Port: 22, Err: errors.New("dial tcp: connect: connection refused")}
	event := connected.statusEventLocked(StatusFailed, connectErr)
	if event.Error != connectErr.UserMessage() {
		t.Fatalf("status event error = %q, want %q", event.Error, connectErr.UserMessage())
	}
	if !strings.Contains(event.Error, "拒绝连接") {
		t.Fatalf("status event error = %q, want actionable Chinese guidance", event.Error)
	}
	plain := connected.statusEventLocked(StatusFailed, errors.New("boom"))
	if plain.Error != "boom" {
		t.Fatalf("unknown cause error = %q, want raw text for logs", plain.Error)
	}
}
