package watches

import (
	"testing"
	"time"
)

func TestObservationAtExpiryDoesNotDispatch(t *testing.T) {
	for _, eof := range []bool{false, true} {
		name := "chunk"
		if eof {
			name = "eof"
		}
		t.Run(name, func(t *testing.T) {
			clock := newTestClock()
			engine, backend := newTestEngine(t, clock)
			ctx := ownerCtx("owner-a")
			attachEngine(t, engine, ctx, "target-1", "identity-1")
			condition := containsCondition("ready")
			if eof {
				condition = Condition{Type: ConditionRegex, Pattern: "ready$"}
			}
			watch, err := engine.Create(ctx, CreateParams{
				TargetID: "target-1", Condition: condition, ExpiresAt: clock.Now().Add(time.Second),
			})
			if err != nil {
				t.Fatal(err)
			}
			if eof {
				if events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); err != nil || len(events) != 0 {
					t.Fatalf("deferred match = %+v, %v", events, err)
				}
			}
			clock.Advance(time.Second)
			var events []Event
			if eof {
				events, err = engine.ObserveEOF(ctx, "target-1", "identity-1", 5)
			} else {
				events, err = engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
			}
			if err != nil || len(events) != 0 {
				t.Fatalf("observation at expiry = %+v, %v", events, err)
			}
			stored, err := engine.Get(ctx, watch.ID)
			if err != nil || stored.Status != StatusExpired {
				t.Fatalf("expired watch = %+v, %v", stored, err)
			}
			sink := &recordingSink{}
			dispatcher, err := NewDispatcher(backend, sink, Options{Now: clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 0 || len(sink.delivered()) != 0 {
				t.Fatalf("late dispatch = %d, %v; deliveries=%+v", delivered, err, sink.delivered())
			}
		})
	}
}

func TestPreExpiryEventDispatchesAfterExpiryWithoutRearming(t *testing.T) {
	clock := newTestClock()
	engine, backend := newTestEngine(t, clock)
	ctx := ownerCtx("owner-a")
	attachEngine(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{
		TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true, ExpiresAt: clock.Now().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); err != nil || len(events) != 1 {
		t.Fatalf("before-expiry fire = %+v, %v", events, err)
	}
	clock.Advance(time.Second)
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 1 || len(sink.delivered()) != 1 {
		t.Fatalf("before-expiry event dispatch = %d, %v", delivered, err)
	}
	stored, err := engine.Get(ctx, watch.ID)
	if err != nil || stored.Status != StatusExpired {
		t.Fatalf("repeat watch after expiry ack = %+v, %v", stored, err)
	}
}
