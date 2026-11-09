package session

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func progressIs(state string, value int) func(AttentionEvent) bool {
	return func(event AttentionEvent) bool {
		p := event.Progress
		return p != nil && p.State == state && p.Value != nil && *p.Value == value
	}
}

func TestAttentionCarriesProgress(t *testing.T) {
	recorder := newAttentionRecorder()
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: screenTerminals(), Emitter: recorder, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	session, err := manager.Connect(ctx, Asset{ID: "docker-progress", Kind: KindDocker})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	recorder.wait(t, tab.ID, "initial idle", attentionIs(base.SessionIdle, base.ConfidenceSignal, AttentionSourceObserved))

	if err := channel.emit([]byte("\x1b]133;C\x07downloading\x1b]9;4;1;40\x07")); err != nil {
		t.Fatal(err)
	}
	shown := recorder.wait(t, tab.ID, "progress", progressIs(base.ProgressNormal, 40))
	if shown.State != base.SessionRunning {
		t.Fatalf("progress changed the state: %+v", shown)
	}
	listed, err := manager.ListAttentionFor(ctx)
	if err != nil || len(listed) != 1 || !progressIs(base.ProgressNormal, 40)(listed[0]) {
		t.Fatalf("listed %+v (%v), want the progress", listed, err)
	}

	// An error report asks for the user like a notification.
	if err := channel.emit([]byte("\x1b]9;4;2\x07")); err != nil {
		t.Fatal(err)
	}
	failed := recorder.wait(t, tab.ID, "progress error", progressIs(base.ProgressError, 40))
	if failed.State != base.SessionNeedsMe || failed.Reason != "progress_error" || failed.Confidence != base.ConfidenceSignal {
		t.Fatalf("progress error state %+v", failed)
	}

	// The command end clears the progress.
	if err := channel.emit([]byte("\x1b]133;D;1\x07")); err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, tab.ID, "progress cleared", func(event AttentionEvent) bool {
		return event.State == base.SessionJustFinished && event.Progress == nil
	})
}

func TestHelperProgressErrorIsExplicitAttention(t *testing.T) {
	value := 70
	event := attentionFromState(&base.SessionState{
		State: base.SessionNeedsMe, Confidence: base.ConfidenceSignal, Reason: "progress_error",
		Progress: &base.SessionProgress{State: base.ProgressError, Value: &value},
	}, AttentionSourceHelper)
	if event.State != base.SessionNeedsMe || !progressIs(base.ProgressError, 70)(event) {
		t.Fatalf("event %+v", event)
	}
	value = 10
	if *event.Progress.Value != 70 {
		t.Fatal("the event shares the helper state's progress")
	}
}
