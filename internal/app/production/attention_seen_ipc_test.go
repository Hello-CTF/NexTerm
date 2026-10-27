package production

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
