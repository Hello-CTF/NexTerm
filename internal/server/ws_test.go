package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

	hub.Send("client-c1", Frame{Kind: FrameJSON, Data: []byte(`{"type":"delta","text":"ok"}`)})
	connection, _, err = websocket.Dial(ctx, channelURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	messageType, data, err = connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || !json.Valid(data) || !strings.Contains(string(data), "delta") {
		t.Fatalf("replay frame = type %v data %s", messageType, data)
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
