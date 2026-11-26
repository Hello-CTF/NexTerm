package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A repeat watch acknowledged inside the minimum interval stays matched; the
// re-arm sweep arms it once the interval passed, only for the calling owner,
// and the fire cap settles it as expired with a visible reason.
func TestAgentWatchDatabaseRepeatPolicy(t *testing.T) {
	_, backend := openAgentWatchFixture(t)
	policy := store.AgentWatchRepeatPolicy{MinInterval: 30 * time.Second, MaxFires: 2}
	owners := map[string]context.Context{}
	for _, owner := range []string{"owner-a", "owner-b"} {
		ctx := ipc.WithUserID(context.Background(), owner)
		owners[owner] = ctx
		target := attachWatchTarget(t, backend, ctx, "target-1", "identity-1")
		row := testWatchRow("watch-repeat-"+owner, "target-1")
		row.Repeat = true
		created, err := backend.AgentWatchCreate(ctx, row, target.Generation)
		if err != nil {
			t.Fatal(err)
		}
		event, err := backend.AgentWatchFire(ctx, fireParams(created, "delivery-repeat-"+owner+"-1", 10))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.AgentWatchEventAckPolicy(ctx, event.DeliveryID, event.Revision, 3000, policy); err != nil {
			t.Fatal(err)
		}
		if got, err := backend.AgentWatchGet(ctx, created.ID); err != nil || got.Status != store.AgentWatchStatusMatched {
			t.Fatalf("%s: watch after an early ack = %+v, %v", owner, got, err)
		}
	}
	ctxA := owners["owner-a"]
	// The fire was at 2000 ms; the interval ends at 32000 ms.
	if count, err := backend.AgentWatchRearmDue(ctxA, 31999, policy); err != nil || count != 0 {
		t.Fatalf("re-arm before the interval = %d, %v", count, err)
	}
	if count, err := backend.AgentWatchRearmDue(ctxA, 32000, policy); err != nil || count != 1 {
		t.Fatalf("re-arm after the interval = %d, %v", count, err)
	}
	armed, err := backend.AgentWatchGet(ctxA, "watch-repeat-owner-a")
	if err != nil || armed.Status != store.AgentWatchStatusArmed || armed.DeliveryID != "" {
		t.Fatalf("re-armed watch = %+v, %v", armed, err)
	}
	if other, err := backend.AgentWatchGet(owners["owner-b"], "watch-repeat-owner-b"); err != nil || other.Status != store.AgentWatchStatusMatched {
		t.Fatalf("another owner's watch was re-armed: %+v, %v", other, err)
	}

	second := fireParams(armed, "delivery-repeat-owner-a-2", 20)
	second.Now = 40000
	event, err := backend.AgentWatchFire(ctxA, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAckPolicy(ctxA, event.DeliveryID, event.Revision, 40001, policy); err != nil {
		t.Fatal(err)
	}
	capped, err := backend.AgentWatchGet(ctxA, "watch-repeat-owner-a")
	if err != nil || capped.Status != store.AgentWatchStatusExpired || capped.LastError != store.AgentWatchRepeatLimitReason {
		t.Fatalf("watch at the fire cap = %+v, %v", capped, err)
	}
	if count, err := backend.AgentWatchRearmDue(ctxA, 100000, policy); err != nil || count != 0 {
		t.Fatalf("re-arm of a capped watch = %d, %v", count, err)
	}
}
