package takeover

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
)

func TestTakeoverSendsLiteralAngleTextUnchanged(t *testing.T) {
	want := `echo '<DIV>Hi</DIV>' "1 < 2 > 0"`
	args, err := json.Marshal(map[string]string{"keys": want})
	if err != nil {
		t.Fatal(err)
	}
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", string(args))),
		toolCallMessage(doneCall("typed")),
	)
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitClosed(t, stream)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != want {
		t.Fatalf("writes = %q, want %q", h.aiWrites, want)
	}
}

func TestTakeoverRefusesUnsupportedKeyChordWithoutWriting(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"<alt+b>"}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK || !strings.Contains(result.Text, "<lt>") || !strings.Contains(result.Text, "paste=true") {
		t.Fatalf("unsupported chord result = %+v", result)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatalf("unsupported chord wrote %q", h.aiWrites)
	}
}

func TestManagerActiveTracksTakeoverOwnership(t *testing.T) {
	h := newHarness(t, sequence())
	if h.manager.Active("tab") {
		t.Fatal("tab reported active before any takeover")
	}
	token, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	if !h.manager.Active("tab") {
		t.Fatal("entered takeover not reported active")
	}
	if h.manager.Active("other") {
		t.Fatal("unrelated tab reported active")
	}
	if err := h.manager.Exit(context.Background(), "tab", token, ""); err != nil {
		t.Fatal(err)
	}
	if h.manager.Active("tab") {
		t.Fatal("exited takeover still reported active")
	}
	if _, err := h.manager.Enter(context.Background(), "tab"); err != nil {
		t.Fatal(err)
	}
	h.manager.Preempt("tab")
	if h.manager.Active("tab") {
		t.Fatal("preempted takeover still reported active")
	}
}
