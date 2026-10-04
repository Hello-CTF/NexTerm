package server

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/coder/websocket"
)

func realHubChannelServer(t *testing.T, webSocket WebSocketConfig) (*hub.Hub, *HubAdapter, string) {
	t.Helper()
	shared := hub.New(hub.Options{QueueBytes: 64 << 20})
	t.Cleanup(func() { _ = shared.Close() })
	adapter := NewHubAdapter(shared)
	config := testConfig(t, false)
	config.Channels = adapter
	config.ChannelStats = adapter.Stats
	config.Environment.Streams = adapter.StreamFactory()
	config.WebSocket = webSocket
	_, httpServer := newTestHTTP(t, config)
	return shared, adapter, strings.Replace(httpServer.URL, "http", "ws", 1)
}

func TestRealHubWriteTimeoutReplaysInFlightFrameOnReconnect(t *testing.T) {
	shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{WriteTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const channelID = "write-timeout-replay"
	producer, err := shared.Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if err := producer.SendBinary(ctx, []byte("first")); err != nil {
		t.Fatal(err)
	}
	huge := bytes.Repeat([]byte{0xab}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("after")); err != nil {
		t.Fatal(err)
	}

	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, data, err := stuck.Read(ctx); err != nil || string(data) != "first" {
		t.Fatalf("first frame = %q, %v", data, err)
	}
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	_ = stuck.CloseNow()

	reconnected, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	reconnected.SetReadLimit(32 << 20)
	_, data, err := reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, huge) {
		t.Fatalf("in-flight frame after reconnect = %d bytes, want the original %d bytes", len(data), len(huge))
	}
	_, data, err = reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("frame after in-flight replay = %q", data)
	}
}

func TestRealHubPongFailureReplaysInFlightFrameOnReconnect(t *testing.T) {
	shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{
		KeepAlive: 50 * time.Millisecond, PingTimeout: 100 * time.Millisecond, WriteTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const channelID = "pong-failure-replay"
	producer, err := shared.Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	huge := bytes.Repeat([]byte{0xcd}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("after")); err != nil {
		t.Fatal(err)
	}

	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	_ = stuck.CloseNow()

	reconnected, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	reconnected.SetReadLimit(32 << 20)
	_, data, err := reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, huge) {
		t.Fatalf("in-flight frame after pong-failure reconnect = %d bytes, want the original %d bytes", len(data), len(huge))
	}
	_, data, err = reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("frame after in-flight replay = %q", data)
	}
}

func TestRealHubDrainingChannelSurvivesUntilInFlightAck(t *testing.T) {
	shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{WriteTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const channelID = "draining-inflight"
	producer, err := shared.Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("terminal")); err != nil {
		t.Fatal(err)
	}
	huge := bytes.Repeat([]byte{0xef}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}

	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	stats := adapter.Stats()
	if stats.PendingChannels != 1 {
		t.Fatalf("draining channel disappeared with an in-flight frame: %+v", stats)
	}
	_ = stuck.CloseNow()

	reconnected, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	reconnected.SetReadLimit(32 << 20)
	_, data, err := reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, huge) {
		t.Fatalf("draining in-flight frame = %d bytes, want the original %d bytes", len(data), len(huge))
	}
	waitFor(t, func() bool {
		stats := adapter.Stats()
		return stats.LiveChannels == 0 && stats.PendingChannels == 0
	})
}
