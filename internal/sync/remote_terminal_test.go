package sync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	gosync "sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// bridgeWSFixture adds a GET /fleet/devices/{id}/bridge echo route on top of remoteAPIFixture's real account surface:
// session validation goes through the fixture's session middleware (missing/invalid cookie is always 401),
// and binary messages are echoed verbatim after a successful upgrade.
type bridgeWSFixture struct {
	*remoteAPIFixture
	failNextDial atomic.Bool
	sawCSRF      atomic.Bool
	sawStream    atomic.Bool
	sawResume    atomic.Bool

	// streams, when set, makes the route answer stream requests as an rstream-capable agent behind the relay
	// would: the stream header, then an echoing stream per OPEN.
	streams *rstream.Server
	mu      gosync.Mutex
	bridges []*websocket.Conn
	done    chan struct{}
}

func newBridgeWSFixture(t *testing.T) *bridgeWSFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inner := &remoteAPIFixture{db: db, accounts: account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))), mfaTickets: map[string]string{}}
	fixture := &bridgeWSFixture{remoteAPIFixture: inner, done: make(chan struct{})}
	t.Cleanup(func() { close(fixture.done) })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", inner.serveLogin)
	mux.HandleFunc("POST /auth/totp/login", inner.serveTOTPLogin)
	mux.Handle("GET /auth/me", inner.session(http.HandlerFunc(inner.serveMe)))
	mux.Handle("GET /auth/dek", inner.session(http.HandlerFunc(inner.serveDEK)))
	mux.Handle("POST /sync/v2/push", inner.session(http.HandlerFunc(inner.serveOK)))
	mux.Handle("GET /fleet/devices/{id}/bridge", inner.session(http.HandlerFunc(fixture.serveBridge)))
	inner.Server = httptest.NewServer(mux)
	t.Cleanup(inner.Server.Close)
	return fixture
}

func (f *bridgeWSFixture) serveBridge(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeaderName) != "" {
		f.sawCSRF.Store(true)
	}
	if r.URL.Query().Get("stream") == "1" {
		f.sawStream.Store(true)
	}
	if r.URL.Query().Get("resume") == "1" {
		f.sawResume.Store(true)
	}
	if f.streams != nil && r.URL.Query().Get("stream") == "1" {
		f.serveStreamBridge(w, r)
		return
	}
	if f.failNextDial.CompareAndSwap(true, false) {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问"))
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	for {
		messageType, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if err := conn.Write(r.Context(), messageType, payload); err != nil {
			return
		}
	}
}

func TestDialDeviceBridgeRoundTrip(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo read = %q, %v; want ping", buffer, err)
	}
	if !fixture.sawCSRF.Load() {
		t.Fatal("bridge dial must carry the CSRF header like the HTTP client")
	}
	// The dial offers a stream carrier; a server that does not answer with the stream header gets a raw bridge,
	// which the echo above exercised.
	if !fixture.sawStream.Load() {
		t.Fatal("bridge dial did not offer a stream carrier")
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (cached session reused)", got)
	}
}

// TestDialDeviceBridgeReLoginAfterRevoke: after the server revokes the session, the first dial with the cached cookie gets a 401,
// and remoteCall's dropSession re-login loop must re-login with the saved password and complete the dial.
func TestDialDeviceBridgeReLoginAfterRevoke(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	userID := fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.RevokeUserSessions(context.Background(), userID, ""); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "pong" {
		t.Fatalf("echo read = %q, %v; want pong", buffer, err)
	}
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (re-login after 401)", got)
	}
}

func TestDialDeviceBridgeForbiddenPassthrough(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	fixture.failNextDial.Store(true)
	if _, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1", ""); err == nil {
		t.Fatal("forbidden dial must fail")
	} else if errors.Is(err, errSessionExpired) {
		t.Fatalf("403 must not be misclassified as session expiry: %v", err)
	}
}

func TestDialDeviceBridgeWithoutSessionIs401(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client, err := newRemoteClient(RemoteConfig{URL: fixture.URL, Username: "alice", Password: "alice-pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.dialDeviceBridge(context.Background(), "dev-1", ""); !errors.Is(err, errSessionExpired) {
		t.Fatalf("dial without login = %v, want errSessionExpired", err)
	}
}

func (f *bridgeWSFixture) serveStreamBridge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(rstream.RelayStreamHeader, "1")
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.bridges = append(f.bridges, conn)
	f.mu.Unlock()
	carrier := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
	stream, resumed, err := f.streams.Accept(r.Context(), carrier, r.URL.Query().Get("purpose"))
	if err != nil {
		return
	}
	if !resumed {
		_, _ = io.Copy(stream, stream)
		_ = stream.Close()
		return
	}
	// The resumed stream is served by the handler of its first bridge; this carrier lives until it is replaced.
	select {
	case <-stream.(*rstream.Stream).Done():
	case <-f.done:
	}
}

// A server that answers with the stream header gets a resumable stream: after the websocket drops, the dialer
// re-dials a resume bridge and the connection continues without losing bytes.
func TestDialDeviceBridgeStreamResumesAfterDrop(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.streams = rstream.NewServer(rstream.ServerOptions{})
	t.Cleanup(func() { _ = fixture.streams.Close() })
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	conn, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1", "forward")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, ok := conn.(*rstream.Stream); !ok {
		t.Fatalf("bridge = %T, want a resumable stream", conn)
	}
	echo := func(message string) {
		t.Helper()
		if _, err := conn.Write([]byte(message)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(message))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != message {
			t.Fatalf("echo = %q, %v; want %q", got, err, message)
		}
	}
	echo("before the drop")
	fixture.mu.Lock()
	first := fixture.bridges[0]
	fixture.mu.Unlock()
	first.CloseNow()
	echo("after the drop")
	if !fixture.sawResume.Load() {
		t.Fatal("the dialer did not re-dial a resume bridge")
	}
}
