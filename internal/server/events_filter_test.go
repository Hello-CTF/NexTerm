package server

import (
	"context"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	fleetserver "github.com/ProbiusOfficial/NexTerm/internal/fleet/server"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestEventBrokerSubscriberFilter(t *testing.T) {
	broker := NewEventBroker()
	filtered, unsubscribeFiltered, err := broker.subscribe(0, false, false, func(event ipc.Event) bool {
		status, ok := event.Payload.(ipc.DeviceStatusEvent)
		return ok && status.Online
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFiltered()
	unfiltered, unsubscribeUnfiltered, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeUnfiltered()

	ctx := context.Background()
	if err := ipc.Emit(ctx, broker, ipc.TopicDeviceStatus, ipc.DeviceStatusEvent{DeviceID: "d-1", Online: true}); err != nil {
		t.Fatal(err)
	}
	if err := ipc.Emit(ctx, broker, ipc.TopicDeviceStatus, ipc.DeviceStatusEvent{DeviceID: "d-2", Online: false}); err != nil {
		t.Fatal(err)
	}
	if err := ipc.Emit(ctx, broker, ipc.TopicSessionStatus, map[string]any{"sessionId": "s-1"}); err != nil {
		t.Fatal(err)
	}

	if data := <-filtered.queue; !strings.Contains(string(data), `"deviceId":"d-1"`) {
		t.Fatalf("filtered event = %s", data)
	}
	if len(filtered.queue) != 0 {
		t.Fatalf("filtered subscriber received rejected events: %d queued", len(filtered.queue))
	}
	for _, want := range []string{`"deviceId":"d-1"`, `"deviceId":"d-2"`, `"sessionId":"s-1"`} {
		data := <-unfiltered.queue
		if !strings.Contains(string(data), want) {
			t.Fatalf("unfiltered event %s = %s", want, data)
		}
	}
}

func TestEventBrokerReplayAppliesFilter(t *testing.T) {
	broker := NewEventBroker()
	ctx := context.Background()
	if err := ipc.Emit(ctx, broker, ipc.TopicDeviceStatus, ipc.DeviceStatusEvent{DeviceID: "d-1", Online: true}); err != nil {
		t.Fatal(err)
	}
	if err := ipc.Emit(ctx, broker, ipc.TopicDeviceStatus, ipc.DeviceStatusEvent{DeviceID: "d-2", Online: true}); err != nil {
		t.Fatal(err)
	}

	subscriber, unsubscribe, err := broker.subscribe(0, true, false, func(event ipc.Event) bool {
		status, ok := event.Payload.(ipc.DeviceStatusEvent)
		return ok && status.DeviceID == "d-2"
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(subscriber.queue) != 1 {
		t.Fatalf("replayed %d events, want 1", len(subscriber.queue))
	}
	if data := <-subscriber.queue; !strings.Contains(string(data), `"deviceId":"d-2"`) {
		t.Fatalf("replayed event = %s", data)
	}
}

func TestDeviceStatusFilterScopesToOwner(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, userID := range []string{"u-1", "u-2"} {
		if _, err := database.DB().ExecContext(context.Background(), `INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
VALUES(?,?,'user','x','active',1,1)`, userID, userID); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ deviceID, ownerID string }{{"dev-1", "u-1"}, {"dev-2", "u-2"}} {
		if _, err := database.DB().ExecContext(context.Background(), `INSERT INTO user_device(id, user_id, name, kind, created_at)
VALUES(?,?,'box','agent',1)`, row.deviceID, row.ownerID); err != nil {
			t.Fatal(err)
		}
	}
	fleet, err := fleetserver.New(fleetserver.Config{DB: database.DB(), Accounts: account.New(database.DB())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fleet.Close() })
	server := &Server{fleet: fleet}

	if filter := server.deviceStatusFilter(&account.Identity{UserID: "u-admin", Role: account.RoleSuperadmin}); filter != nil {
		t.Fatal("superadmin must not be filtered")
	}
	if filter := server.deviceStatusFilter(nil); filter != nil {
		t.Fatal("identity-less connection (open deployment) must not be filtered")
	}
	if filter := (&Server{}).deviceStatusFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}); filter != nil {
		t.Fatal("filter without fleet service must not be installed")
	}

	filter := server.deviceStatusFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser})
	if filter == nil {
		t.Fatal("plain user filter is nil")
	}
	cases := []struct {
		name  string
		event ipc.Event
		want  bool
	}{
		{"own device", ipc.Event{Event: ipc.TopicDeviceStatus, Payload: ipc.DeviceStatusEvent{DeviceID: "dev-1", Online: true}}, true},
		{"other device", ipc.Event{Event: ipc.TopicDeviceStatus, Payload: ipc.DeviceStatusEvent{DeviceID: "dev-2", Online: true}}, false},
		{"unknown device", ipc.Event{Event: ipc.TopicDeviceStatus, Payload: ipc.DeviceStatusEvent{DeviceID: "missing", Online: true}}, false},
		{"other topic", ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]any{"sessionId": "s-1"}}, true},
		{"foreign payload", ipc.Event{Event: ipc.TopicDeviceStatus, Payload: map[string]any{"deviceId": "dev-1"}}, false},
	}
	for _, tc := range cases {
		if got := filter(tc.event); got != tc.want {
			t.Fatalf("%s: filter = %v, want %v", tc.name, got, tc.want)
		}
	}
}
