package takeover

import (
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
