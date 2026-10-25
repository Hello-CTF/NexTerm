package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

type channelWrite struct {
	TabID    string `json:"tabId"`
	Data     []int  `json:"data"`
	ClientID string `json:"clientId"`
}

func channelInputFrameFor(tabID string, data []byte) []byte {
	frame := append([]byte{channelInputFrame, byte(len(tabID))}, tabID...)
	return append(frame, data...)
}

func newChannelInputFixture(t *testing.T) (url string, writes func() []channelWrite) {
	t.Helper()
	shared := hub.New(hub.Options{})
	t.Cleanup(func() { _ = shared.Close() })
	adapter := NewHubAdapter(shared)
	config := testConfig(t, false)
	config.Channels = adapter
	config.ChannelStats = adapter.Stats
	var mu sync.Mutex
	var got []channelWrite
	err := ipc.RegisterNested(config.Dispatcher, "terminal_write", func(_ context.Context, _ *ipc.Call, in channelWrite) (any, error) {
		mu.Lock()
		got = append(got, in)
		mu.Unlock()
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, httpServer := newTestHTTP(t, config)
	return strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/channel/input-test?client=c1", func() []channelWrite {
		mu.Lock()
		defer mu.Unlock()
		return append([]channelWrite(nil), got...)
	}
}

func TestChannelSocketInputFramesApplyInOrder(t *testing.T) {
	url, writes := newChannelInputFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{channelInputSubprotocol, channelSubprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != channelInputSubprotocol {
		t.Fatalf("subprotocol = %q", conn.Subprotocol())
	}
	for _, chunk := range []string{"a", "bc", "\x1b[A"} {
		if err := conn.Write(ctx, websocket.MessageBinary, channelInputFrameFor("tab-1", []byte(chunk))); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(writes()) == 3 })
	for i, want := range []string{"a", "bc", "\x1b[A"} {
		got := writes()[i]
		var text []byte
		for _, b := range got.Data {
			text = append(text, byte(b))
		}
		if got.TabID != "tab-1" || got.ClientID != "c1" || string(text) != want {
			t.Fatalf("write %d = %+v, want %q", i, got, want)
		}
	}
}

func TestChannelSocketWithoutInputSubprotocolIgnoresFrames(t *testing.T) {
	url, writes := newChannelInputFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{channelSubprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != channelSubprotocol {
		t.Fatalf("subprotocol = %q", conn.Subprotocol())
	}
	_ = conn.Write(ctx, websocket.MessageBinary, channelInputFrameFor("tab-1", []byte("x")))
	time.Sleep(100 * time.Millisecond)
	if len(writes()) != 0 {
		t.Fatal("input applied without negotiation")
	}
}

func TestParseChannelInputRejectsMalformed(t *testing.T) {
	for _, frame := range [][]byte{nil, {1}, {1, 0, 'x'}, {1, 5, 'a', 'b'}, {2, 1, 'a', 'b'}, {1, 1, 'a'}} {
		if _, _, ok := parseChannelInput(frame); ok {
			t.Fatalf("accepted %v", frame)
		}
	}
}

func TestInputLimiterBurstAndRefill(t *testing.T) {
	now := time.Unix(0, 0)
	limiter := newInputLimiter(inputLimits{FrameBurst: 3, FrameRate: 2, ByteBurst: 10, ByteRate: 10}, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if !limiter.allow(1) {
			t.Fatalf("frame %d rejected within burst", i)
		}
	}
	if limiter.allow(1) {
		t.Fatal("frame beyond burst accepted")
	}
	now = now.Add(500 * time.Millisecond)
	if !limiter.allow(1) {
		t.Fatal("frame rejected after refill")
	}
	now = now.Add(10 * time.Second)
	if limiter.allow(11) {
		t.Fatal("frame above byte burst accepted")
	}
	if !limiter.allow(10) {
		t.Fatal("frame within byte burst rejected")
	}
}

func TestChannelSocketClosedWhenInputFloods(t *testing.T) {
	prev := channelInputLimits
	channelInputLimits = inputLimits{FrameBurst: 3, FrameRate: 0.001, ByteBurst: 1 << 20, ByteRate: 1 << 20}
	t.Cleanup(func() { channelInputLimits = prev })
	url, writes := newChannelInputFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{channelInputSubprotocol, channelSubprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for i := 0; i < 6; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, channelInputFrameFor("tab-1", []byte("x"))); err != nil {
			break
		}
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close status = %v (%v), want policy violation", websocket.CloseStatus(err), err)
	}
	if n := len(writes()); n > 3 {
		t.Fatalf("applied %d frames, budget was 3", n)
	}
}
