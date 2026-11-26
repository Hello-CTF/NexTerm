package tools

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestValidateSubscribeArgs(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	got, err := ValidateSubscribeArgs(now, TerminalSubscribeArgs{On: "finished"})
	if err != nil || got.On != "just_finished" || !got.ExpiresAt.Equal(now.Add(time.Hour)) || got.Repeat {
		t.Fatalf("finished = %+v, %v", got, err)
	}
	got, err = ValidateSubscribeArgs(now, TerminalSubscribeArgs{On: "output", Pattern: "ERROR \\d+", Regex: true, Repeat: true, ExpiresInMinutes: 5})
	if err != nil || got.On != "output" || !got.Regex || !got.Repeat || !got.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("output = %+v, %v", got, err)
	}
	for name, args := range map[string]TerminalSubscribeArgs{
		"unknown on":          {On: "running"},
		"state with pattern":  {On: "needs_me", Pattern: "x"},
		"output without text": {On: "output"},
		"expiry too long":     {On: "exited", ExpiresInMinutes: 24*60 + 1},
		"negative expiry":     {On: "exited", ExpiresInMinutes: -1},
	} {
		if _, err := ValidateSubscribeArgs(now, args); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A subscription starts unattended runs later, so creating one is never
// silent, and an unattended run cannot create another.
func TestSubscribeNeedsConfirmationAndIsDeniedUnattended(t *testing.T) {
	ruling := guard.ClassifyTool(SubscribeTool, []byte(`{"on":"finished"}`), guard.Config{})
	if ruling.Risk != guard.NeedsConfirm {
		t.Fatalf("ruling = %+v", ruling)
	}
	decision := guard.Decide(guard.Config{Mode: guard.Unattended}, ruling, guard.NewMemory())
	if decision.Action != guard.ActionDeny {
		t.Fatalf("unattended decision = %+v", decision)
	}
}
