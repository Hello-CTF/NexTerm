package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

func TestRealHubPendingReplayLiveAndReceiverClose(t *testing.T) {
	var closed atomic.Int32
	shared := hub.New(hub.Options{OnChannelClose: func(string) { closed.Add(1) }})
	t.Cleanup(func() { _ = shared.Close() })
	adapter := NewHubAdapter(shared)
	config := testConfig(t, false)
	config.Channels = adapter
	config.ChannelStats = adapter.Stats
	config.Environment.Streams = adapter.StreamFactory()
	_, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const channelID = "real-hub-channel"
	producer, err := config.Environment.Streams.OpenBinary(ctx, ipc.ChannelRef{ID: channelID})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if err := producer.SendBinary(ctx, []byte("replay-1")); err != nil {
		t.Fatal(err)
	}
	if err := shared.SendBinary(ctx, channelID, []byte("replay-2")); err != nil {
		t.Fatal(err)
	}

	channelURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/channel/" + channelID
	connection, _, err := websocket.Dial(ctx, channelURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 1 })
	if err := shared.SendBinary(ctx, channelID, []byte("live-3")); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"replay-1", "replay-2", "live-3"} {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if messageType != websocket.MessageBinary || string(data) != expected {
			t.Fatalf("frame = type %v data %q, want %q", messageType, data, expected)
		}
	}
	if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 && closed.Load() == 1 })

	terminal := []byte(`{"type":"error","message":"real hub run failed","final":true}`)
	if err := shared.SendJSON(ctx, channelID, terminal); err != nil {
		t.Fatalf("watcher close cancelled background producer: %v", err)
	}
	if adapter.Stats().PendingChannels != 1 {
		t.Fatal("terminal frame was not retained for reconnect")
	}
	connection, _, err = websocket.Dial(ctx, channelURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	messageType, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Final   bool   `json:"final"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || event.Type != "error" || event.Message != "real hub run failed" || !event.Final {
		t.Fatalf("terminal frame = type %v data %+v", messageType, event)
	}
}
