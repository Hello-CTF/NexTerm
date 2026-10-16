package takeover

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestTakeoverModuleRegistersOnlyTakeoverCommands(t *testing.T) {
	h := newHarness(t, sequence())
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	commands := dispatcher.Commands()
	for _, command := range []string{"ai_takeover_enter", "ai_takeover_run", "ai_takeover_resume", "ai_takeover_exit"} {
		found := false
		for _, registered := range commands {
			found = found || registered == command
		}
		if !found {
			t.Errorf("missing %s", command)
		}
	}
}

func TestTakeoverModuleExitStaleRestoredAndValidLifecycle(t *testing.T) {
	h := newHarness(t, sequence())
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(token string) ipc.Response {
		t.Helper()
		args, err := json.Marshal(map[string]string{"tabId": "tab", "token": token, "reason": "test"})
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_takeover_exit", Args: args}, ipc.Environment{})
	}
	assertStale := func(response ipc.Response) {
		t.Helper()
		if response.OK || response.Error == nil {
			t.Fatalf("stale exit response = %+v, want a structured error", response)
		}
		if response.Error.Code != ipc.CodeBadParam || response.Error.Message != ErrStaleOwnership.Error() || response.Error.Detail != nil {
			t.Fatalf("stale exit error = %+v, want safe bad_param %q", response.Error, ErrStaleOwnership.Error())
		}
	}

	assertStale(dispatch("missing-owner-token"))
	token, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	assertStale(dispatch("incorrect-owner-token"))
	if response := dispatch(token); !response.OK || response.Error != nil {
		t.Fatalf("valid exit response = %+v, want success", response)
	}
	assertStale(dispatch(token))
}
