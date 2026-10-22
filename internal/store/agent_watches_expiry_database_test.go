package store_test

import (
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestAgentWatchDatabaseFireHonorsExpiry(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	ctx := watchOwnerCtx("owner-a")
	target := attachWatchTarget(t, backend, ctx, "target-expiry", "identity-1")
	for _, test := range []struct {
		name    string
		expires int64
		fires   bool
	}{
		{name: "before", expires: 2001, fires: true},
		{name: "at", expires: 2000},
		{name: "after", expires: 1999},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := testWatchRow("watch-expiry-"+test.name, target.TargetID)
			row.ExpiresAt = &test.expires
			row.Repeat = true
			created, err := backend.AgentWatchCreate(ctx, row, target.Generation)
			if err != nil {
				t.Fatal(err)
			}
			params := fireParams(created, "delivery-expiry-"+test.name, 5)
			event, err := backend.AgentWatchFire(ctx, params)
			if test.fires {
				if err != nil {
					t.Fatal(err)
				}
				// A legitimate event remains deliverable after expiry, but
				// acknowledging it must not re-arm the expired repeat watch.
				replay := params
				replay.Now = 3000
				if got, err := backend.AgentWatchFire(ctx, replay); err != nil || got.DeliveryID != event.DeliveryID {
					t.Fatalf("late replay = %+v, %v", got, err)
				}
				acked, err := backend.AgentWatchEventAck(ctx, event.DeliveryID, event.Revision, 3000)
				if err != nil || acked.Status != store.AgentWatchEventAcked {
					t.Fatalf("ack before-expiry fire = %+v, %v", acked, err)
				}
			} else {
				if !errors.Is(err, store.ErrAgentWatchState) {
					t.Fatalf("late fire error = %v, want state rejection", err)
				}
				if _, err := backend.AgentWatchEventGet(ctx, params.DeliveryID); !errors.Is(err, store.ErrAgentWatchNotFound) {
					t.Fatalf("late outbox event error = %v, want absent", err)
				}
			}
			watch, err := backend.AgentWatchGet(ctx, created.ID)
			if err != nil || watch.Status != store.AgentWatchStatusExpired {
				t.Fatalf("watch after expiry = %+v, %v", watch, err)
			}
			if !test.fires && (watch.FireSeq != 0 || watch.DeliveryID != "") {
				t.Fatalf("expired watch recorded a fire: %+v", watch)
			}
		})
	}
}
