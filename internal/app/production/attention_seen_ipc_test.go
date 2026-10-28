package production

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// Only a helper that can never share a seen mark is reported with the seen_unsupported reason; a transient
// failure keeps its code but carries no reason, so the client retries instead of marking the state local-only.
func TestAttentionSeenIPCErrorSeparatesPermanentFromTransient(t *testing.T) {
	var permanent *ipc.Error
	if !errors.As(terminalIPCError(durable.ErrSeenUnsupported), &permanent) || permanent.Code != ipc.CodeUnsupported {
		t.Fatalf("permanent = %+v, want code unsupported", permanent)
	}
	detail, ok := permanent.Detail.(map[string]any)
	if !ok || detail["reason"] != "seen_unsupported" {
		t.Fatalf("permanent detail = %#v, want reason seen_unsupported", permanent.Detail)
	}
	var transient *ipc.Error
	if !errors.As(terminalIPCError(durable.ErrUnavailable), &transient) {
		t.Fatalf("transient was not an IPC error")
	}
	if transient.Detail != nil {
		t.Fatalf("transient detail = %#v, want none", transient.Detail)
	}
}

// An unavailable durable helper is transient, so a failed close or list reports a disconnect with the real reason
// instead of "unsupported"; reasons that name a specific remedy keep their own codes.
func TestTerminalIPCErrorMapsUnavailableDurableToDisconnected(t *testing.T) {
	tests := []struct {
		err  error
		code ipc.Code
	}{
		{err: durable.ErrUnavailable, code: ipc.CodeDisconnected},
		{err: fmt.Errorf("%w: dial supervisor: connection refused", durable.ErrUnavailable), code: ipc.CodeDisconnected},
		{err: fmt.Errorf("%w: %w", durable.ErrUnavailable, supervisor.ErrHelperUpgradeRequired), code: ipc.Code("helper_upgrade_required")},
		{err: fmt.Errorf("%w: %w", durable.ErrLimit, durable.ErrUnavailable), code: ipc.Code("durable_limit")},
	}
	for _, test := range tests {
		var got *ipc.Error
		if !errors.As(terminalIPCError(test.err), &got) || got.Code != test.code {
			t.Errorf("terminalIPCError(%v) = %+v, want code %s", test.err, got, test.code)
		}
	}
}
