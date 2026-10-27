package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// A tab outside a helper cannot read its terminal foreground, so its
// detected prompts are never tappable; a read-only spectator never taps.
// A refused tap writes nothing and moves no input owner.
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
	if _, err := manager.AttachSpectator(ctx, tab.ID, SpectateOptions{ClientID: "viewer", ChannelID: "viewer-tap"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.TapPromptFor(ctx, tab.ID, "viewer", PromptTapRequest{Generation: "g", Epoch: 1, Fingerprint: "f"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("tap from a read-only spectator = %v, want ErrForbidden", err)
	}
	if last := tab.LastInputClient(); last != "" {
		t.Fatalf("a refused tap made %q the input owner", last)
	}
	if written := channel.written(); len(written) != 0 {
		t.Fatalf("a refused tap wrote %q", written)
	}
}
