package guard

import (
	"encoding/json"
	"testing"
)

func TestR5SS3HistorySequencesMatchCSIDanger(t *testing.T) {
	t.Parallel()
	for _, keys := range []string{"\x1bOA<enter>", "\x1bOB<enter>", "\x1bOA", "\x1bOB"} {
		t.Run(keys, func(t *testing.T) {
			ruling := ClassifySendKeysWithBuffer(keys, "ls", false, nil)
			if ruling.Risk != Danger {
				t.Fatalf("SS3 history ruling = %v, want Danger", ruling)
			}
			if decision := Decide(Config{Mode: Silent}, ruling, nil); decision.Action != ActionAsk {
				t.Fatalf("silent SS3 history decision = %+v, want ActionAsk", decision)
			}
		})
	}
	args, _ := json.Marshal(map[string]any{"keys": "\x1bOA", "enter": true})
	ruling := ClassifyTool("send_keys", args, Config{Mode: Silent})
	if ruling.Risk != Danger {
		t.Fatalf("send_keys SS3 ruling = %v, want Danger", ruling)
	}
}

func TestR5SS3CursorKeysEditWithoutCorruption(t *testing.T) {
	t.Parallel()
	if ruling := ClassifySendKeysWithBuffer("\x1bOC<enter>", "ls", false, nil); ruling.Risk != Safe {
		t.Fatalf("SS3 right on trailing cursor = %v, want Safe", ruling)
	}
	ruling := ClassifySendKeysWithBuffer("\x1bOHx<enter>", "ls", false, nil)
	if ruling.Risk != NeedsConfirm {
		t.Fatalf("SS3 home then insert = %v, want NeedsConfirm for edited command", ruling)
	}
	commands, controls, _ := EffectiveKeyActions("\x1bOD\x1bOC", "ab", 1, false)
	if len(controls) != 0 {
		t.Fatalf("SS3 cursor keys reported as controls: %v", controls)
	}
	if len(commands) != 0 {
		t.Fatalf("SS3 cursor keys produced commands: %v", commands)
	}
}
