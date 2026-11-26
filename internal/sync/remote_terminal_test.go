package sync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
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

	// relayOn makes the fixture offer the multiplexed relay (GET /relay/config and the websocket carrier);
	// off, both routes answer 404 like a server that predates the relay. bridgeDials counts bridge upgrades.
	relayOn        atomic.Bool
	bridgeDials    atomic.Int32
	relayStreams   atomic.Int32
	sawRelayResume atomic.Bool
	relayRefusal   *relaymux.Reply
	relayLinks     []*relaymux.Link
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
	mux.HandleFunc("GET "+relaymux.ConfigPath, func(w http.ResponseWriter, r *http.Request) {
		if !fixture.relayOn.Load() {
			http.NotFound(w, r)
			return
		}
		inner.session(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSyncJSON(w, http.StatusOK, relaymux.Config{Enabled: true, Version: relaymux.Version, WSPath: relaymux.WSPath})
		})).ServeHTTP(w, r)
	})
	mux.HandleFunc("GET "+relaymux.WSPath, fixture.serveRelay)
	inner.Server = httptest.NewServer(mux)
	t.Cleanup(inner.Server.Close)
	return fixture
}

// serveRelay plays the relay: the Hello token must be a valid login session, and each stream is refused with
// relayRefusal or served like a bridge of the same request.
func (f *bridgeWSFixture) serveRelay(w http.ResponseWriter, r *http.Request) {
	if !f.relayOn.Load() {
		http.NotFound(w, r)
		return
	}
	session, err := relaymux.AcceptWS(w, r)
	if err != nil {
		return
	}
	pending, err := relaymux.AcceptHandshake(context.Background(), session)
	if err != nil {
		return
	}
	if _, err := f.accounts.ValidateSession(context.Background(), pending.Hello.Token); err != nil {
		pending.Refuse(relaymux.Reply{Status: http.StatusUnauthorized, Code: string(ipc.CodeForbidden), Message: "会话无效或缺失，请重新登录"})
		return
	}
	link, err := pending.Accept(relaymux.Reply{})
	if err != nil {
		return
	}
	f.mu.Lock()
	f.relayLinks = append(f.relayLinks, link)
	f.mu.Unlock()
	_ = link.ServeStreams(context.Background(), func(ctx context.Context, incoming *relaymux.IncomingStream) {
		f.relayStreams.Add(1)
		f.mu.Lock()
		refusal := f.relayRefusal
		f.mu.Unlock()
		if refusal != nil {
			incoming.Refuse(*refusal)
			return
		}
		if incoming.Header.Params[relaymux.ParamResume] == "1" {
			f.sawRelayResume.Store(true)
		}
		if f.streams == nil || incoming.Header.Params[relaymux.ParamStream] != "1" {
			conn, err := incoming.Accept(nil)
			if err != nil {
				return
			}
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
			return
		}
		carrier, err := incoming.Accept(map[string]string{relaymux.ParamStream: "1"})
		if err != nil {
			return
		}
		stream, resumed, err := f.streams.Accept(ctx, carrier, incoming.Header.Purpose)
		if err != nil || resumed {
			return
		}
		_, _ = io.Copy(stream, stream)
		_ = stream.Close()
	})
}

func (f *bridgeWSFixture) closeRelayLinks() {
	f.mu.Lock()
	links := f.relayLinks
	f.relayLinks = nil
	f.mu.Unlock()
	for _, link := range links {
		_ = link.Close()
	}
}

func (f *bridgeWSFixture) serveBridge(w http.ResponseWriter, r *http.Request) {
	f.bridgeDials.Add(1)
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

// With the relay offered, device streams go over one relay link instead of a bridge each, and a stream resumes on
// a redialed link after the link drops.
func TestDialDeviceUsesTheRelayWhenOffered(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.relayOn.Store(true)
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
		t.Fatalf("stream = %T, want a resumable stream", conn)
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
	fixture.closeRelayLinks()
	echo("after the drop")
	signal, err := instance.service.Remote().DialDeviceSignal(context.Background(), "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = signal.Close()
	if !fixture.sawRelayResume.Load() {
		t.Fatal("the stream did not resume over the relay")
	}
	if got := fixture.bridgeDials.Load(); got != 0 {
		t.Fatalf("bridge dials = %d, want every stream on the relay", got)
	}
	if got := fixture.relayStreams.Load(); got != 3 {
		t.Fatalf("relay streams = %d, want the open, the resume and the signal", got)
	}
}

// A rejected login on the relay re-logs in like a bridge 401 and the stream then opens on the relay.
func TestDialDeviceRelayReLoginAfterRevoke(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.relayOn.Store(true)
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
	_ = conn.Close()
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (re-login after 401)", got)
	}
	if fixture.bridgeDials.Load() != 0 || fixture.relayStreams.Load() != 1 {
		t.Fatalf("bridge dials %d, relay streams %d; want the stream on the relay", fixture.bridgeDials.Load(), fixture.relayStreams.Load())
	}
}

// A refused relay stream gives the ipc error the bridge gives for the same refusal, and never falls back to it.
func TestDialDeviceRelayRefusalMapsLikeTheBridge(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	// One client meets a server without the relay, the other one with it.
	bridgeClient := newTestInstance(t, true)
	instance := newTestInstance(t, true)
	for _, client := range []*testInstance{bridgeClient, instance} {
		if _, err := client.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
			t.Fatal(err)
		}
	}
	fixture.failNextDial.Store(true)
	_, bridgeErr := bridgeClient.service.Remote().DialDeviceBridge(context.Background(), "dev-1", "")
	fixture.relayOn.Store(true)
	fixture.mu.Lock()
	fixture.relayRefusal = &relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Message: "设备已关闭终端访问"}
	fixture.mu.Unlock()
	dials := fixture.bridgeDials.Load()
	_, relayErr := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1", "")
	var fromBridge, fromRelay *ipc.Error
	if !errors.As(bridgeErr, &fromBridge) || !errors.As(relayErr, &fromRelay) {
		t.Fatalf("bridge error %v, relay error %v; want ipc errors", bridgeErr, relayErr)
	}
	if fromBridge.Code != fromRelay.Code || fromBridge.Message != fromRelay.Message {
		t.Fatalf("relay error %v, bridge error %v", fromRelay, fromBridge)
	}
	if fixture.bridgeDials.Load() != dials {
		t.Fatal("a relay refusal fell back to the bridge")
	}
}

// The refusal mapping shared by bridge upgrades and relay streams.
func TestEndpointRefusalMatchesTheUpgradeMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		rpcErr *ipc.Error
	}{
		{status: http.StatusUnauthorized},
		{status: http.StatusForbidden, body: `{"error":{"code":"forbidden","message":"设备已关闭远程访问"}}`, rpcErr: ipc.NewError(ipc.CodeForbidden, "设备已关闭远程访问")},
		{status: http.StatusConflict, body: `{"error":{"code":"unsupported","message":"设备代理不支持续传通道"}}`, rpcErr: ipc.NewError(ipc.CodeUnsupported, "设备代理不支持续传通道")},
		{status: http.StatusServiceUnavailable},
	} {
		response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}
		upgrade := wsUpgradeError(response, "远端设备桥")
		relay := endpointRefusal(tc.status, tc.rpcErr, "远端设备桥")
		if errors.Is(upgrade, errSessionExpired) != errors.Is(relay, errSessionExpired) || upgrade.Error() != relay.Error() {
			t.Fatalf("status %d: upgrade %v, relay %v", tc.status, upgrade, relay)
		}
	}
}
