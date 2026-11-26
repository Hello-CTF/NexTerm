package watches

import (
	"testing"
	"time"
)

func TestStateWatchFiresOncePerNewState(t *testing.T) {
	clock := newTestClock()
	engine, backend := newTestEngine(t, clock)
	ctx := ownerCtx("owner-1")
	attachEngine(t, engine, ctx, "tab-1", "identity-1")
	current := clock.Now().Add(-time.Minute)
	watch, err := engine.Create(ctx, CreateParams{
		TargetID: "tab-1", ConversationID: "conv-1", Repeat: true, StateSince: current,
		Condition: Condition{Type: ConditionState, Pattern: StateJustFinished},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The state current at creation is already known to the creator.
	if events, err := engine.ObserveState(ctx, "tab-1", "identity-1", StateJustFinished, current); err != nil || len(events) != 0 {
		t.Fatalf("creation-time state fired: %v, %v", events, err)
	}
	// Output never fires or invalidates a state watch.
	if events, err := engine.Observe(ctx, "tab-1", "identity-1", 500, []byte("just_finished\n")); err != nil || len(events) != 0 {
		t.Fatalf("output fired a state watch: %v, %v", events, err)
	}
	// Another state does not fire it.
	later := clock.Now()
	if events, _ := engine.ObserveState(ctx, "tab-1", "identity-1", StateNeedsMe, later); len(events) != 0 {
		t.Fatalf("needs_me fired a just_finished watch: %v", events)
	}
	events, err := engine.ObserveState(ctx, "tab-1", "identity-1", StateJustFinished, later)
	if err != nil || len(events) != 1 {
		t.Fatalf("new state fired %d events: %v", len(events), err)
	}
	if events[0].ConversationID != "conv-1" || events[0].WatchID != watch.ID || events[0].Excerpt != StateJustFinished {
		t.Fatalf("event = %+v", events[0])
	}

	// Re-armed by the acknowledgement, the same state republished (seen,
	// progress, restart) stays quiet; only a newer state fires again.
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, pending[0].DeliveryID, pending[0].Revision, clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if events, _ := engine.ObserveState(ctx, "tab-1", "identity-1", StateJustFinished, later); len(events) != 0 {
		t.Fatalf("republished state fired again: %v", events)
	}
	clock.Advance(time.Second)
	if events, _ := engine.ObserveState(ctx, "tab-1", "identity-1", StateJustFinished, clock.Now()); len(events) != 1 {
		t.Fatalf("a newer state did not fire the repeat watch: %v", events)
	}
	stored, err := engine.Get(ctx, watch.ID)
	if err != nil || stored.Status != StatusMatched || stored.LastError != "" {
		t.Fatalf("watch = %+v, %v", stored, err)
	}
}

func TestStateConditionValidation(t *testing.T) {
	for _, pattern := range []string{StateNeedsMe, StateJustFinished, StateExited} {
		if err := (Condition{Type: ConditionState, Pattern: pattern}).Validate(); err != nil {
			t.Errorf("%s rejected: %v", pattern, err)
		}
	}
	if err := (Condition{Type: ConditionState, Pattern: "running"}).Validate(); err == nil {
		t.Error("an unsupported state was accepted")
	}
}
