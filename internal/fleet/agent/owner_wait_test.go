package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ownerHarness drives channelLoop against a fake server with an injected sleep so backoff is deterministic.
type ownerHarness struct {
	runtime *Runtime
	fake    *fakeFleetServer
	dataDir string

	mu      sync.Mutex
	delays  []time.Duration
	onSleep func(n int)
	dials   int
	script  func(dial int, conn *websocket.Conn) bool // true = handled (connection finished)
}

func newOwnerHarness(t *testing.T) *ownerHarness {
	t.Helper()
	h := &ownerHarness{fake: newFakeFleetServer(t), dataDir: t.TempDir()}
	h.fake.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		h.mu.Lock()
		h.dials++
		n := h.dials
		script := h.script
		h.mu.Unlock()
		if script(n, conn) {
			conn.CloseNow()
			return
		}
		_ = writeServerMessage(conn, serverMessage{Type: "hello_ok"})
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	entries := []BaseURLEntry{{URL: h.fake.server.URL}}
	h.runtime = newTestRuntime(t, h.dataDir, entries, &fakeManager{})
	h.runtime.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	h.runtime.deviceID, h.runtime.secret = "dev", "secret"
	h.runtime.jitter = func() float64 { return 0.5 }
	h.runtime.sleep = func(ctx context.Context, d time.Duration) bool {
		h.mu.Lock()
		h.delays = append(h.delays, d)
		n := len(h.delays)
		hook := h.onSleep
		h.mu.Unlock()
		if hook != nil {
			hook(n)
		}
		return ctx.Err() == nil
	}
	return h
}

func (h *ownerHarness) setScript(script func(int, *websocket.Conn) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.script = script
}

func (h *ownerHarness) sleeps() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.delays...)
}

func (h *ownerHarness) start(t *testing.T) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.runtime.channelLoop(ctx) }()
	return func() { cancel(); <-done }
}

func ownerFrame(conn *websocket.Conn) bool {
	_ = writeServerMessage(conn, serverMessage{Type: "error", Code: "forbidden", Reason: ErrorCodeOwnerUnavailable, Message: "owner"})
	return true
}

func TestOldStyleForbiddenStillStopsAgent(t *testing.T) {
	h := newOwnerHarness(t)
	h.setScript(func(_ int, conn *websocket.Conn) bool {
		_ = writeServerMessage(conn, serverMessage{Type: "error", Code: "forbidden", Message: "x"})
		return true
	})
	stop := h.start(t)
	defer stop()
	select {
	case <-h.runtime.revokeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("forbidden did not stop the agent")
	}
	if len(h.sleeps()) != 0 {
		t.Fatalf("forbidden must not enter backoff: %v", h.sleeps())
	}
}

func TestOwnerUnavailableRetriesWithCappedBackoffAndRecovers(t *testing.T) {
	h := newOwnerHarness(t)
	var connected sync.WaitGroup
	connected.Add(1)
	var once sync.Once
	h.runtime.onChannel = func(up bool) {
		if up {
			once.Do(connected.Done)
		}
	}
	markerSeen := make(chan bool, 1)
	h.setScript(func(dial int, conn *websocket.Conn) bool {
		if dial <= 7 {
			return ownerFrame(conn)
		}
		return false
	})
	h.onSleep = func(n int) {
		if n == 1 {
			_, ok := ReadOwnerWait(h.dataDir)
			markerSeen <- ok
		}
	}
	stop := h.start(t)
	defer stop()
	connected.Wait()
	select {
	case <-h.runtime.revokeCh:
		t.Fatal("owner_unavailable must not revoke the agent")
	default:
	}
	if ok := <-markerSeen; !ok {
		t.Fatal("status marker missing while waiting")
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	got := h.sleeps()
	if len(got) != len(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delays = %v, want %v", got, want)
		}
	}
	if _, ok := ReadOwnerWait(h.dataDir); ok {
		t.Fatal("status marker not cleared after recovery")
	}
}

func TestOwnerUnavailableHTTPCodeIsNotRevocation(t *testing.T) {
	err := &ServerError{Status: http.StatusForbidden, Code: ErrorCodeOwnerUnavailable}
	if isForbidden(err) || !isOwnerUnavailable(err) {
		t.Fatal("owner_unavailable over 403 must be temporary")
	}
	if !isForbidden(&ServerError{Status: http.StatusForbidden, Code: "forbidden"}) {
		t.Fatal("plain 403 must remain a revocation")
	}
}

func TestSyncWaitsOnOwnerUnavailableAndResumes(t *testing.T) {
	h := newOwnerHarness(t)
	now := time.Unix(1000, 0)
	h.runtime.now = func() time.Time { return now }
	h.fake.mu.Lock()
	h.fake.syncOwnerUnavailable = true
	h.fake.mu.Unlock()
	ctx := context.Background()
	h.runtime.syncOnce(ctx)
	if !h.runtime.ownerWaiting() {
		t.Fatal("sync should enter owner wait")
	}
	before := len(h.fake.syncs())
	h.runtime.syncOnce(ctx)
	if len(h.fake.syncs()) != before {
		t.Fatal("sync must not contact the server before the backoff elapses")
	}
	h.fake.mu.Lock()
	h.fake.syncOwnerUnavailable = false
	h.fake.mu.Unlock()
	now = now.Add(time.Minute)
	h.runtime.syncOnce(ctx)
	if h.runtime.ownerWaiting() || len(h.fake.syncs()) != before+1 {
		t.Fatal("sync should resume and clear the wait once the backoff elapsed")
	}
	select {
	case <-h.runtime.revokeCh:
		t.Fatal("must not revoke")
	default:
	}
	if _, ok := ReadOwnerWait(h.dataDir); ok {
		t.Fatal("marker should be cleared")
	}
}

func TestKickFrameThenReconnectAndRevokedAfterOwnerUnavailableStops(t *testing.T) {
	h := newOwnerHarness(t)
	h.setScript(func(dial int, conn *websocket.Conn) bool {
		switch dial {
		case 1: // connected, then kicked because the owner was disabled
			_ = writeServerMessage(conn, serverMessage{Type: "hello_ok"})
			_ = writeServerMessage(conn, serverMessage{Type: "revoke", Reason: ErrorCodeOwnerUnavailable})
			return true
		case 2: // still disabled
			return ownerFrame(conn)
		default: // device revoked meanwhile
			_ = writeServerMessage(conn, serverMessage{Type: "error", Code: "forbidden", Message: "revoked"})
			return true
		}
	})
	stop := h.start(t)
	defer stop()
	select {
	case <-h.runtime.revokeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("real revocation after owner_unavailable must stop the agent")
	}
	if got := len(h.sleeps()); got != 2 {
		t.Fatalf("expected 2 owner waits before revocation, got %d", got)
	}
}
