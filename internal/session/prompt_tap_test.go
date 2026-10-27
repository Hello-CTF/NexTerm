package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// A tab outside a helper cannot read its terminal foreground, so its
// detected prompts are never tappable and a tap writes nothing.
func TestPromptTapNeedsAHelperTab(t *testing.T) {
	manager, tab, channel, _ := openCommandTestTab(t, &fakeCommandSink{}, "user-1")
	ctx := ipc.WithUserID(context.Background(), "user-1")
	err := manager.TapPromptFor(ctx, tab.ID, "client-a", PromptTapRequest{Generation: "g", Epoch: 1, Fingerprint: "f", Option: 0})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("tap on a plain tab = %v, want ErrUnsupported", err)
	}
	if err := manager.TapPromptFor(ctx, tab.ID, "client-a", PromptTapRequest{Epoch: 1, Fingerprint: "f"}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("tap without a generation = %v, want ErrInvalidOptions", err)
	}
	if written := channel.written(); len(written) != 0 {
		t.Fatalf("a refused tap wrote %q", written)
	}
}
