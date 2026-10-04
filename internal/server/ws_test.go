package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

var errTestReceiverDetached = errors.New("test receiver detached")

type replayHub struct {
	mu       sync.Mutex
	channels map[string]*replayChannel
	closed   int
}

type replayChannel struct {
	mu         sync.Mutex
	changed    chan struct{}
	queue      []Frame
	bound      bool
	generation uint64
	owner      *replayHub
}

type replayReceiver struct {
	channel    *replayChannel
	generation uint64
	once       sync.Once
}

func newReplayHub() *replayHub {
	return &replayHub{channels: make(map[string]*replayChannel)}
}

func (h *replayHub) channel(id string) *replayChannel {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel := h.channels[id]
	if channel == nil {
		channel = &replayChannel{changed: make(chan struct{}), owner: h}
		h.channels[id] = channel
	}
	return channel
}

func (h *replayHub) Send(id string, frame Frame) {
	channel := h.channel(id)
	channel.mu.Lock()
	frame.Data = append([]byte(nil), frame.Data...)
	channel.queue = append(channel.queue, frame)
	channel.signal()
	channel.mu.Unlock()
}

func (h *replayHub) BindChannel(id string) (ChannelReceiver, error) {
	if id == "" {
		return nil, errors.New("empty channel")
	}
	channel := h.channel(id)
	channel.mu.Lock()
	channel.generation++
	generation := channel.generation
	channel.bound = true
	channel.signal()
	channel.mu.Unlock()
	return &replayReceiver{channel: channel, generation: generation}, nil
}

func (h *replayHub) Stats() ChannelStats {
	h.mu.Lock()
	channels := make([]*replayChannel, 0, len(h.channels))
	for _, channel := range h.channels {
		channels = append(channels, channel)
	}
	h.mu.Unlock()
	var stats ChannelStats
	for _, channel := range channels {
		channel.mu.Lock()
		if channel.bound {
			stats.LiveChannels++
		} else if len(channel.queue) > 0 {
			stats.PendingChannels++
		}
		channel.mu.Unlock()
	}
	return stats
}

func (c *replayChannel) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (r *replayReceiver) Next(ctx context.Context) (Frame, error) {
	for {
		r.channel.mu.Lock()
		if !r.channel.bound || r.channel.generation != r.generation {
			r.channel.mu.Unlock()
			return Frame{}, errTestReceiverDetached
		}
		if len(r.channel.queue) > 0 {
			frame := r.channel.queue[0]
			r.channel.queue = r.channel.queue[1:]
			r.channel.mu.Unlock()
			return frame, nil
		}
		changed := r.channel.changed
		r.channel.mu.Unlock()
		select {
		case <-ctx.Done():
			return Frame{}, ctx.Err()
		case <-changed:
		}
	}
}

func (r *replayReceiver) Close() error {
	r.once.Do(func() {
		r.channel.mu.Lock()
		if r.channel.bound && r.channel.generation == r.generation {
			r.channel.bound = false
			r.channel.generation++
			r.channel.signal()
			r.channel.owner.mu.Lock()
			r.channel.owner.closed++
			r.channel.owner.mu.Unlock()
		}
		r.channel.mu.Unlock()
	})
	return nil
}

func TestWebSocketReplayLiveBoundaryPreservesArrivalOrder(t *testing.T) {
	hub := newReplayHub()
	config := testConfig(t, false)
	config.Channels = hub
	config.ChannelStats = hub.Stats
	_, httpServer := newTestHTTP(t, config)
	const channelID = "ordered-channel"
	hub.Send(channelID, Frame{Sequence: 1, Kind: FrameBinary, Data: []byte("replay-1")})
	hub.Send(channelID, Frame{Sequence: 3, Kind: FrameBinary, Data: []byte("replay-3")})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/channel/"+channelID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return hub.Stats().LiveChannels == 1 })
	hub.Send(channelID, Frame{Sequence: 2, Kind: FrameBinary, Data: []byte("live-2")})
	hub.Send(channelID, Frame{Sequence: 4, Kind: FrameBinary, Data: []byte("live-4")})

	for _, expected := range []string{"replay-1", "replay-3", "live-2", "live-4"} {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if messageType != websocket.MessageBinary || string(data) != expected {
			t.Fatalf("frame = type %v data %q, want binary %q", messageType, data, expected)
		}
	}
}

func TestWebSocketChannelPendingReplayAndFrameTypes(t *testing.T) {
	hub := newReplayHub()
	config := testConfig(t, false)
	config.Channels = hub
	config.ChannelStats = hub.Stats
	_, httpServer := newTestHTTP(t, config)
	channelURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/channel/client-c1"
	hub.Send("client-c1", Frame{Kind: FrameBinary, Data: []byte{0, 1, 0xff, 0x7f}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, channelURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	messageType, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary || string(data) != string([]byte{0, 1, 0xff, 0x7f}) {
		t.Fatalf("early frame = type %v data %v", messageType, data)
	}
	if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return hub.Stats().LiveChannels == 0 })

	hub.Send("client-c1", Frame{Sequence: 5, Kind: FrameJSON, Data: []byte(`{"type":"error","message":"backend run failed","final":true}`)})
	if hub.Stats().PendingChannels != 1 {
		t.Fatal("closing the watcher cancelled the pending background stream")
	}
	connection, _, err = websocket.Dial(ctx, channelURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	messageType, data, err = connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var terminal struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Final   bool   `json:"final"`
	}
	if err := json.Unmarshal(data, &terminal); err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || terminal.Type != "error" || terminal.Message != "backend run failed" || !terminal.Final {
		t.Fatalf("terminal replay = type %v data %+v", messageType, terminal)
	}
}

func TestWebSocketEventsEnvelopeAndUnsubscribe(t *testing.T) {
	server, httpServer := newTestHTTP(t, testConfig(t, false))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://localhost:1420"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]string{"sessionId": "s1", "status": "connected"}}); err != nil {
		t.Fatal(err)
	}
	messageType, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("event type = %v", messageType)
	}
	var envelope struct {
		Event   string            `json:"event"`
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Event != string(ipc.TopicSessionStatus) || envelope.Payload["sessionId"] != "s1" {
		t.Fatalf("event = %+v", envelope)
	}
	if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 0 })
}

func TestPumpSocketTeardownDoesNotWaitForPeerCloseFrame(t *testing.T) {
	server, err := New(testConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	parked := make(chan struct{})
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	server.readGate = func() {
		close(parked)
		<-gate
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		server.pumpSocket(ctx, connection, func(ctx context.Context) (websocket.MessageType, []byte, error) {
			<-ctx.Done()
			return 0, nil, ctx.Err()
		})
		close(returned)
	}))
	defer httpServer.Close()
	defer cancel()
	defer openGate()
	connection, _, err := websocket.Dial(context.Background(), strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/channel/parked", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("read loop did not park")
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	openGate()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpSocket teardown blocked on the peer's close frame")
	}
}

func TestCloseContextBoundsBlockedChannelBinder(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	config := testConfig(t, false)
	config.Channels = ChannelBinderFunc(func(string) (ChannelReceiver, error) {
		close(entered)
		<-block
		return nil, errors.New("binder released")
	})
	server, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/channel/blocked", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("binder was not reached")
	}
	closeCtx, stopClose := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopClose()
	if err := server.CloseContext(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v", err)
	}
	close(block)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
