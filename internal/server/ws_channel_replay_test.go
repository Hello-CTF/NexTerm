package server

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/coder/websocket"
)

type observedChannelReceiver struct {
	ChannelReceiver
	next chan<- Frame
}

func (r *observedChannelReceiver) Next(ctx context.Context) (Frame, error) {
	frame, err := r.ChannelReceiver.Next(ctx)
	if err == nil {
		select {
		case r.next <- frame:
		default:
		}
	}
	return frame, err
}

func realHubChannelServer(t *testing.T, webSocket WebSocketConfig, next chan<- Frame) (*Server, *hub.Hub, *HubAdapter, string) {
	t.Helper()
	shared := hub.New(hub.Options{QueueBytes: 64 << 20})
	t.Cleanup(func() { _ = shared.Close() })
	adapter := NewHubAdapter(shared)
	config := testConfig(t, false)
	config.Channels = ChannelBinderFunc(func(id string) (ChannelReceiver, error) {
		receiver, err := adapter.BindChannel(id)
		if err != nil {
			return nil, err
		}
		return &observedChannelReceiver{ChannelReceiver: receiver, next: next}, nil
	})
	config.ChannelStats = adapter.Stats
	config.Environment.Streams = adapter.StreamFactory()
	config.WebSocket = webSocket
	server, httpServer := newTestHTTP(t, config)
	return server, shared, adapter, strings.Replace(httpServer.URL, "http", "ws", 1)
}

func awaitNextSequence(t *testing.T, frames <-chan Frame, want uint64) {
	t.Helper()
	select {
	case frame := <-frames:
		if frame.Sequence != want {
			t.Fatalf("next frame sequence = %d, want %d", frame.Sequence, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("frame %d was not checked out", want)
	}
}

func requireHubQueue(t *testing.T, shared *hub.Hub, frames, queuedBytes int) {
	t.Helper()
	stats := shared.Stats()
	if stats.QueuedFrames != frames || stats.QueuedBytes != queuedBytes {
		t.Fatalf("hub queue = %d frames %d bytes, want %d frames %d bytes", stats.QueuedFrames, stats.QueuedBytes, frames, queuedBytes)
	}
}

func TestRealHubWriteTimeoutReplaysInFlightFrameOnReconnect(t *testing.T) {
	nextSeen := make(chan Frame, 8)
	server, shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{KeepAlive: -1, WriteTimeout: 2 * time.Second}, nextSeen)
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

	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, data, err := stuck.Read(ctx); err != nil || string(data) != "first" {
		t.Fatalf("first frame = %q, %v", data, err)
	}
	awaitNextSequence(t, nextSeen, 1)
	waitFor(t, func() bool { return shared.Stats().QueuedFrames == 0 })
	server.webSocket.WriteTimeout = time.Nanosecond
	huge := bytes.Repeat([]byte{0xab}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("after")); err != nil {
		t.Fatal(err)
	}
	awaitNextSequence(t, nextSeen, 2)
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	requireHubQueue(t, shared, 2, len(huge)+len("after"))
	server.webSocket.WriteTimeout = 2 * time.Second
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
	nextSeen := make(chan Frame, 8)
	_, shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{
		KeepAlive: 50 * time.Millisecond, PingTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second,
	}, nextSeen)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const channelID = "pong-failure-replay"
	producer, err := shared.Producer(channelID)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	pingSeen := make(chan struct{}, 1)
	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			select {
			case pingSeen <- struct{}{}:
			default:
			}
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _, _ = stuck.Read(ctx)
	}()
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 1 })
	select {
	case <-pingSeen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	huge := bytes.Repeat([]byte{0xcd}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("after")); err != nil {
		t.Fatal(err)
	}
	awaitNextSequence(t, nextSeen, 1)
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	requireHubQueue(t, shared, 2, len(huge)+len("after"))
	_ = stuck.CloseNow()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stuck client read did not stop")
	}

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
	nextSeen := make(chan Frame, 8)
	server, shared, adapter, baseURL := realHubChannelServer(t, WebSocketConfig{KeepAlive: -1, WriteTimeout: 2 * time.Second}, nextSeen)
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

	stuck, _, err := websocket.Dial(ctx, baseURL+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, data, err := stuck.Read(ctx); err != nil || string(data) != "terminal" {
		t.Fatalf("terminal frame = %q, %v", data, err)
	}
	awaitNextSequence(t, nextSeen, 1)
	waitFor(t, func() bool { return shared.Stats().QueuedFrames == 0 })
	server.webSocket.WriteTimeout = time.Nanosecond
	huge := bytes.Repeat([]byte{0xef}, 16<<20)
	if err := producer.SendBinary(ctx, huge); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	awaitNextSequence(t, nextSeen, 2)
	waitFor(t, func() bool { return adapter.Stats().LiveChannels == 0 })
	stats := adapter.Stats()
	if stats.PendingChannels != 1 {
		t.Fatalf("draining channel disappeared with an in-flight frame: %+v", stats)
	}
	requireHubQueue(t, shared, 1, len(huge))
	server.webSocket.WriteTimeout = 2 * time.Second
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
