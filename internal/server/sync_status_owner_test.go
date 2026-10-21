package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/coder/websocket"
)

func TestSyncStatusRPCOwnerLiveAndReplay(t *testing.T) {
	fixture, _, syncService := newAIOwnerRPCFixture(t)
	ctx := context.Background()
	alice, err := fixture.accounts.CreateUser(ctx, "alice", "Alice", "alice-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := fixture.accounts.CreateUser(ctx, "bob", "Bob", "bob-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	aliceSession := fixture.login(t, "alice", "alice-pw-123")
	bobSession := fixture.login(t, "bob", "bob-pw-123")
	eventsURL := "ws" + strings.TrimPrefix(fixture.http.URL, "http") + "/ws/events"
	aliceEvents, status := dialAccountWebSocket(t, eventsURL, aliceSession)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("A events handshake = %d, want 101", status)
	}
	bobEvents, status := dialAccountWebSocket(t, eventsURL, bobSession)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("B events handshake = %d, want 101", status)
	}
	waitFor(t, func() bool { return fixture.server.Events().SubscriberCount() == 2 })

	call := fixture.call(t, http.MethodPost, "/rpc", map[string]any{
		"cmd": syncservice.CommandPreferencesSet,
		"args": map[string]any{"args": map[string]any{
			"set": map[string]any{"appearance.themeMode": "light"},
		}},
	}, aliceSession, aliceSession.csrf, nil)
	if call.status != http.StatusOK || call.body["ok"] != true {
		t.Fatalf("A preference mutation status=%d body=%v", call.status, call.body)
	}
	aliceView, err := syncService.PreferenceView(ipc.WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(aliceView.Overrides["appearance.themeMode"]); got != `"light"` {
		t.Fatalf("A preference override = %s, want light", got)
	}
	bobView, err := syncService.PreferenceView(ipc.WithUserID(ctx, bob.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := bobView.Overrides["appearance.themeMode"]; exists {
		t.Fatal("A preference mutation changed B's account data")
	}

	if err := fixture.server.Events().Emit(context.Background(), ipc.Event{
		Event: ipc.TopicSyncStatus, Payload: ipc.SyncStatusEvent{},
	}); err != nil {
		t.Fatal(err)
	}
	const barrier = "sync-status-owner-live-replay-barrier"
	if err := fixture.server.Events().Emit(context.Background(), ipc.Event{
		Event: ipc.TopicAppError, Payload: ipc.AppErrorEvent{Code: "test", Message: barrier},
	}); err != nil {
		t.Fatal(err)
	}

	assertEvents := func(t *testing.T, conn *websocket.Conn, actor string, wantStatuses int) {
		t.Helper()
		frames, found := readWebSocketUntil(conn, barrier, 2*time.Second)
		if !found {
			t.Fatalf("%s did not receive the public barrier; frames = %q", actor, frames)
		}
		statuses := 0
		for _, frame := range frames {
			var envelope struct {
				Event   ipc.Topic       `json:"event"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(frame, &envelope); err != nil {
				t.Fatalf("%s received malformed event frame %q: %v", actor, frame, err)
			}
			if envelope.Event != ipc.TopicSyncStatus {
				continue
			}
			statuses++
			var payload ipc.SyncStatusEvent
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				t.Fatalf("%s received malformed sync status %q: %v", actor, frame, err)
			}
			if payload.OwnerID != alice.ID {
				t.Fatalf("%s received foreign or ownerless sync status %q", actor, frame)
			}
		}
		if statuses != wantStatuses {
			t.Fatalf("%s sync status count = %d, want %d; frames = %q", actor, statuses, wantStatuses, frames)
		}
	}
	t.Run("live-A-receives", func(t *testing.T) {
		assertEvents(t, aliceEvents, "live A", 1)
	})
	t.Run("live-B-does-not-receive", func(t *testing.T) {
		assertEvents(t, bobEvents, "live B", 0)
	})
	_ = aliceEvents.Close(websocket.StatusNormalClosure, "")
	_ = bobEvents.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return fixture.server.Events().SubscriberCount() == 0 })

	for _, actor := range []struct {
		name         string
		session      *accountTestSession
		wantStatuses int
	}{
		{name: "replay-A-receives", session: aliceSession, wantStatuses: 1},
		{name: "replay-B-does-not-receive", session: bobSession, wantStatuses: 0},
	} {
		t.Run(actor.name, func(t *testing.T) {
			conn, status := dialAccountWebSocket(t, eventsURL+"?since=0", actor.session)
			if status != http.StatusSwitchingProtocols {
				t.Fatalf("replay handshake = %d, want 101", status)
			}
			defer conn.CloseNow()
			assertEvents(t, conn, actor.name, actor.wantStatuses)
		})
	}
}
