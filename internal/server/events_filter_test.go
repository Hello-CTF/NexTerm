package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

func TestEventFilterScopesDeviceStatusAndNotificationToOwner(t *testing.T) {
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
	fleet, err := fleetserver.New(fleetserver.Config{DB: database.GORM(), Accounts: account.New(database.GORM())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fleet.Close() })
	server := &Server{fleet: fleet}

	if filter := server.eventFilter(&account.Identity{UserID: "u-admin", Role: account.RoleSuperadmin}); filter == nil {
		t.Fatal("superadmin must be scoped to owned devices")
	} else if filter(ipc.Event{Event: ipc.TopicDeviceStatus, Payload: ipc.DeviceStatusEvent{DeviceID: "dev-1", Online: true}}) {
		t.Fatal("superadmin received another user's device event")
	}
	if filter := server.eventFilter(nil); filter != nil {
		t.Fatal("identity-less connection (open deployment) must not be filtered")
	}
	if filter := (&Server{}).eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}); filter == nil {
		t.Fatal("signed-in connection must install the filter even without fleet service")
	} else {
		if filter(ipc.Event{Event: ipc.TopicDeviceStatus, Payload: ipc.DeviceStatusEvent{DeviceID: "dev-1", Online: true}}) {
			t.Fatal("device events must be fail-closed when no fleet service is configured")
		}
		if filter(ipc.Event{Event: ipc.TopicAINotification, Payload: ipc.AINotificationEvent{OwnerID: "u-2", Text: "x"}}) {
			t.Fatal("AI notification must stay owner-scoped without fleet service")
		}
	}

	filter := server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser})
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
		{"ownerless session status", ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]any{"sessionId": "s-1"}}, false},
		{"foreign payload", ipc.Event{Event: ipc.TopicDeviceStatus, Payload: map[string]any{"deviceId": "dev-1"}}, false},
		{"own notification", ipc.Event{Event: ipc.TopicAINotification, Payload: ipc.AINotificationEvent{OwnerID: "u-1", Text: "构建完成"}}, true},
		{"other user notification", ipc.Event{Event: ipc.TopicAINotification, Payload: ipc.AINotificationEvent{OwnerID: "u-2", Text: "构建完成"}}, false},
		{"ownerless notification", ipc.Event{Event: ipc.TopicAINotification, Payload: ipc.AINotificationEvent{Text: "构建完成"}}, false},
		{"foreign notification payload", ipc.Event{Event: ipc.TopicAINotification, Payload: map[string]any{"ownerId": "u-1"}}, false},
		{"own watch tool data", ipc.Event{Event: ipc.TopicAIWatchToolData, Payload: watches.ToolDataEvent{OwnerID: "u-1", Data: watches.NewToolData(watches.Event{Kind: watches.EventKind, DeliveryID: "d-1"})}}, true},
		{"other user watch tool data", ipc.Event{Event: ipc.TopicAIWatchToolData, Payload: watches.ToolDataEvent{OwnerID: "u-2", Data: watches.NewToolData(watches.Event{Kind: watches.EventKind, DeliveryID: "d-1"})}}, false},
		{"ownerless watch tool data", ipc.Event{Event: ipc.TopicAIWatchToolData, Payload: watches.ToolDataEvent{Data: watches.NewToolData(watches.Event{Kind: watches.EventKind, DeliveryID: "d-1"})}}, false},
		{"foreign watch tool data payload", ipc.Event{Event: ipc.TopicAIWatchToolData, Payload: map[string]any{"ownerId": "u-1"}}, false},
		{"own layout", ipc.Event{Event: ipc.TopicLayoutChanged, Payload: ipc.LayoutChangedEvent{Revision: 1}, OwnerID: "u-1"}, true},
		{"other user layout", ipc.Event{Event: ipc.TopicLayoutChanged, Payload: ipc.LayoutChangedEvent{Revision: 1}, OwnerID: "u-2"}, false},
		{"ownerless layout", ipc.Event{Event: ipc.TopicLayoutChanged, Payload: ipc.LayoutChangedEvent{Revision: 1}}, false},
		{"malformed layout", ipc.Event{Event: ipc.TopicLayoutChanged, Payload: map[string]any{"revision": 1}, OwnerID: "u-1"}, false},
		{"own inventory", ipc.Event{Event: ipc.TopicInventoryChanged, Payload: ipc.InventoryChangedEvent{Source: "ssh-config", GroupID: "g-1"}, OwnerID: "u-1"}, true},
		{"other user inventory", ipc.Event{Event: ipc.TopicInventoryChanged, Payload: ipc.InventoryChangedEvent{Source: "ssh-config", GroupID: "g-2"}, OwnerID: "u-2"}, false},
		{"ownerless inventory", ipc.Event{Event: ipc.TopicInventoryChanged, Payload: ipc.InventoryChangedEvent{Source: "ssh-config", GroupID: "g-1"}}, false},
		{"malformed inventory", ipc.Event{Event: ipc.TopicInventoryChanged, Payload: map[string]any{"source": "ssh-config"}, OwnerID: "u-1"}, false},
		{"own sync status", ipc.Event{Event: ipc.TopicSyncStatus, Payload: ipc.SyncStatusEvent{OwnerID: "u-1"}}, true},
		{"other user sync status", ipc.Event{Event: ipc.TopicSyncStatus, Payload: ipc.SyncStatusEvent{OwnerID: "u-2"}}, false},
		{"unscoped sync status", ipc.Event{Event: ipc.TopicSyncStatus, Payload: ipc.SyncStatusEvent{}}, false},
		{"legacy sync status payload", ipc.Event{Event: ipc.TopicSyncStatus, Payload: struct{}{}}, false},
		{"malformed sync status payload", ipc.Event{Event: ipc.TopicSyncStatus, Payload: map[string]any{}}, false},
		{"legacy sync round complete", ipc.Event{Event: ipc.Topic("sync://round-complete"), Payload: struct{}{}}, true},
		{"malformed sync round complete", ipc.Event{Event: ipc.Topic("sync://round-complete"), Payload: map[string]any{}}, false},
	}
	for _, tc := range cases {
		if got := filter(tc.event); got != tc.want {
			t.Fatalf("%s: filter = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEventFilterConnectProgressScopedToAssetOwner(t *testing.T) {
	owners := map[string]string{"asset-1": "u-1", "asset-2": "u-2"}
	server := &Server{assetOwner: func(_ context.Context, id string) (string, error) {
		owner, ok := owners[id]
		if !ok {
			return "", errors.New("not found")
		}
		return owner, nil
	}}
	filter := server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser})
	if filter == nil {
		t.Fatal("plain user filter is nil")
	}
	cases := []struct {
		name  string
		event ipc.Event
		want  bool
	}{
		{"own asset", ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-1", Stage: "dial", Host: "bastion.internal", Port: 22}}, true},
		{"other asset", ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-2", Stage: "dial", Host: "bastion.internal", Port: 22}}, false},
		{"unknown asset", ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "missing", Stage: "dial"}}, false},
		{"foreign payload", ipc.Event{Event: ipc.TopicConnectProgress, Payload: map[string]any{"assetId": "asset-1"}}, false},
		{"own helper", ipc.Event{Event: ipc.TopicHelperProgress, Payload: ipc.HelperProgressEvent{SessionID: "s-1", AssetID: "asset-1", Stage: "upload", Percent: 40}}, true},
		{"other helper", ipc.Event{Event: ipc.TopicHelperProgress, Payload: ipc.HelperProgressEvent{SessionID: "s-2", AssetID: "asset-2", Stage: "upload"}}, false},
		{"unknown helper", ipc.Event{Event: ipc.TopicHelperProgress, Payload: ipc.HelperProgressEvent{AssetID: "missing"}}, false},
		{"malformed helper", ipc.Event{Event: ipc.TopicHelperProgress, Payload: map[string]any{"assetId": "asset-1"}}, false},
		{"ownerless session status", ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]any{"sessionId": "s-1"}}, false},
	}
	for _, tc := range cases {
		if got := filter(tc.event); got != tc.want {
			t.Fatalf("%s: filter = %v, want %v", tc.name, got, tc.want)
		}
	}
	if adminFilter := server.eventFilter(&account.Identity{UserID: "u-admin", Role: account.RoleSuperadmin}); adminFilter == nil ||
		adminFilter(ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-1", Stage: "dial"}}) {
		t.Fatal("superadmin must be scoped to owned assets")
	}
}

func TestEventFilterSessionAndTerminalOwnerMetadata(t *testing.T) {
	server := &Server{}
	actors := []*account.Identity{
		{UserID: "u-1", Role: account.RoleUser},
		{UserID: "u-2", Role: account.RoleUser},
		{UserID: "admin", Role: account.RoleSuperadmin},
	}
	sensitive := []struct {
		topic   ipc.Topic
		payload any
	}{
		{ipc.TopicSessionStatus, session.StatusEvent{SessionID: "s-1"}},
		{ipc.TopicTerminalExit, session.ExitEvent{TabID: "t-1"}},
		{ipc.TopicTerminalThrottled, session.ThrottleEvent{TabID: "t-1"}},
		{ipc.TopicTerminalControl, session.ControlEvent{TabID: "t-1"}},
	}
	for _, actor := range actors {
		filter := server.eventFilter(actor)
		for _, eventType := range sensitive {
			for _, ownerID := range []string{"", "u-1", "u-2", "admin"} {
				event := ipc.Event{Event: eventType.topic, Payload: eventType.payload, OwnerID: ownerID}
				want := ownerID != "" && ownerID == actor.UserID
				if got := filter(event); got != want {
					t.Fatalf("actor %s topic %s owner %q: filter = %v, want %v", actor.UserID, eventType.topic, ownerID, got, want)
				}
			}
			for _, malformed := range []any{nil, map[string]any{"id": "forged"}} {
				event := ipc.Event{Event: eventType.topic, Payload: malformed, OwnerID: actor.UserID}
				if filter(event) {
					t.Fatalf("actor %s topic %s accepted malformed %T payload", actor.UserID, eventType.topic, malformed)
				}
			}
		}
	}
}

func TestEventFilterPublicAllowlistAndUnknownTopic(t *testing.T) {
	filter := (&Server{}).eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser})
	for _, topic := range []ipc.Topic{ipc.TopicAppError, ipc.TopicUpdateProgress} {
		if !filter(ipc.Event{Event: topic, Payload: struct{}{}}) {
			t.Fatalf("public topic %s was rejected", topic)
		}
	}
	if filter(ipc.Event{Event: ipc.Topic("unknown://sensitive"), Payload: struct{}{}, OwnerID: "u-1"}) {
		t.Fatal("unknown topic was delivered by the default branch")
	}
}

func TestInventoryChangedBrokerDeliveryRespectsSubscriberOwner(t *testing.T) {
	broker := NewEventBroker()
	server := &Server{}
	alice, unsubscribeAlice, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeAlice()
	bob, unsubscribeBob, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-2", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeBob()

	if err := ipc.Emit(ipc.WithUserID(context.Background(), "u-1"), broker, ipc.TopicInventoryChanged, ipc.InventoryChangedEvent{Source: "ssh-config", GroupID: "g-1"}); err != nil {
		t.Fatal(err)
	}
	if len(alice.queue) != 1 || len(bob.queue) != 0 {
		t.Fatalf("owner delivery queues: alice=%d bob=%d", len(alice.queue), len(bob.queue))
	}
	if data := <-alice.queue; !strings.Contains(string(data), `"source":"ssh-config"`) {
		t.Fatalf("inventory payload = %s", data)
	}
	if err := ipc.Emit(context.Background(), broker, ipc.TopicInventoryChanged, ipc.InventoryChangedEvent{Source: "ssh-config", GroupID: "g-ownerless"}); err != nil {
		t.Fatal(err)
	}
	if len(alice.queue) != 0 || len(bob.queue) != 0 {
		t.Fatal("ownerless inventory event reached a logged-in subscriber")
	}
	if err := ipc.Emit(ipc.WithUserID(context.Background(), "u-2"), broker, ipc.TopicInventoryChanged, ipc.InventoryChangedEvent{Source: "kubernetes", GroupID: "g-2"}); err != nil {
		t.Fatal(err)
	}
	if len(alice.queue) != 0 || len(bob.queue) != 1 {
		t.Fatalf("second owner delivery queues: alice=%d bob=%d", len(alice.queue), len(bob.queue))
	}
}
