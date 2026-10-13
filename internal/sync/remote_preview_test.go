package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	stdsync "sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// remoteSessionFixture adds a canned replica of the remote session shell routes on top of remoteAPIFixture's real account surface:
// session validation goes through the session middleware (missing/invalid cookie is always 401), write endpoints
// enforce CSRF; the feed streaming WS records handshake headers and echoes binary messages.
type remoteSessionFixture struct {
	*remoteAPIFixture

	mu            stdsync.Mutex
	created       map[string]any
	previewCreate map[string]any
	deleted       []string
	sawFeedCSRF   atomic.Bool
	sawFeedCookie atomic.Bool
}

func newRemoteSessionFixture(t *testing.T) *remoteSessionFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inner := &remoteAPIFixture{db: db, accounts: account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))), mfaTickets: map[string]string{}}
	fixture := &remoteSessionFixture{remoteAPIFixture: inner}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", inner.serveLogin)
	mux.HandleFunc("POST /auth/totp/login", inner.serveTOTPLogin)
	mux.Handle("GET /auth/me", inner.session(http.HandlerFunc(inner.serveMe)))
	mux.Handle("GET /auth/dek", inner.session(http.HandlerFunc(inner.serveDEK)))
	mux.Handle("POST /sync/v2/push", inner.session(http.HandlerFunc(inner.serveOK)))
	mux.Handle("POST /share/remote-sessions", inner.session(inner.csrf(http.HandlerFunc(fixture.serveRemoteSessionCreate))))
	mux.Handle("DELETE /share/remote-sessions/{id}", inner.session(inner.csrf(http.HandlerFunc(fixture.serveRemoteSessionDelete))))
	mux.Handle("POST /share/previews", inner.session(inner.csrf(http.HandlerFunc(fixture.servePreviewCreate))))
	mux.Handle("GET /share/remote-sessions/{id}/feed", inner.session(http.HandlerFunc(fixture.serveFeed)))
	inner.Server = httptest.NewServer(mux)
	t.Cleanup(inner.Server.Close)
	return fixture
}

func (f *remoteSessionFixture) serveRemoteSessionCreate(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	f.mu.Lock()
	f.created = request
	f.mu.Unlock()
	writeSyncJSON(w, http.StatusOK, map[string]any{"session_id": "shell-1", "cols": 100, "rows": 40})
}

func (f *remoteSessionFixture) serveRemoteSessionDelete(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.deleted = append(f.deleted, r.PathValue("id"))
	f.mu.Unlock()
	writeSyncJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (f *remoteSessionFixture) servePreviewCreate(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(err))
		return
	}
	f.mu.Lock()
	f.previewCreate = request
	f.mu.Unlock()
	writeSyncJSON(w, http.StatusOK, map[string]any{
		"id": "plink-1", "owner_id": "u-1", "session_id": "shell-1",
		"created_at": 1, "expires_at": 2, "token": "tok-abc",
	})
}

func (f *remoteSessionFixture) serveFeed(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeaderName) != "" {
		f.sawFeedCSRF.Store(true)
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		f.sawFeedCookie.Store(true)
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

// TestRemoteSessionTypedMethods pins the typed-method contract of remote shell/preview creation: request payloads
// are shaped like the server routes (snake_case), and responses decode fully (including the one-time token).
func TestRemoteSessionTypedMethods(t *testing.T) {
	fixture := newRemoteSessionFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	remote := instance.service.Remote()
	ctx := context.Background()

	shell, err := remote.RemoteSessionCreate(ctx, "web-01", "asset-1", 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	if shell.SessionID != "shell-1" || shell.Cols != 100 || shell.Rows != 40 {
		t.Fatalf("shell = %+v", shell)
	}
	if got := fixture.created["title"]; got != "web-01" {
		t.Fatalf("create title = %v", got)
	}
	if got := fixture.created["asset_id"]; got != "asset-1" {
		t.Fatalf("create asset_id = %v", got)
	}
	if got := fixture.created["cols"]; got != float64(100) {
		t.Fatalf("create cols = %v", got)
	}

	link, err := remote.CreatePreviewLink(ctx, shell.SessionID, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if link.Token != "tok-abc" || link.ID != "plink-1" || link.SessionID != "shell-1" || link.ExpiresAt != 2 {
		t.Fatalf("link = %+v", link)
	}
	if got := fixture.previewCreate["session_id"]; got != "shell-1" {
		t.Fatalf("preview session_id = %v", got)
	}
	if got := fixture.previewCreate["ttl_ms"]; got != float64(60_000) {
		t.Fatalf("preview ttl_ms = %v", got)
	}

	if _, err := remote.RemoteSessionDelete(ctx, "shell-1"); err != nil {
		t.Fatal(err)
	}
	if len(fixture.deleted) != 1 || fixture.deleted[0] != "shell-1" {
		t.Fatalf("deleted = %v", fixture.deleted)
	}

	base, err := remote.ServerBaseURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if base != fixture.URL {
		t.Fatalf("base = %q, want %q", base, fixture.URL)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (cached session reused)", got)
	}
}

// TestRemoteSessionFeedDialRoundTrip pins the feed WS dialing: the handshake carries cookie+CSRF,
// message-level reads and writes work after connecting, and the cached session is reused without repeated logins.
func TestRemoteSessionFeedDialRoundTrip(t *testing.T) {
	fixture := newRemoteSessionFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().RemoteSessionFeedDial(context.Background(), "shell-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("out-bytes")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageBinary || string(payload) != "out-bytes" {
		t.Fatalf("echo = (%v, %q)", kind, payload)
	}
	if !fixture.sawFeedCSRF.Load() || !fixture.sawFeedCookie.Load() {
		t.Fatalf("feed handshake must carry cookie+CSRF (cookie=%v csrf=%v)", fixture.sawFeedCookie.Load(), fixture.sawFeedCSRF.Load())
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (cached session reused)", got)
	}
}

// TestRemoteSessionFeedDialReLoginAfterRevoke: after the server revokes the session, the first dial with the cached cookie gets a 401,
// and remoteCall's dropSession re-login loop must re-login and complete the dial.
func TestRemoteSessionFeedDialReLoginAfterRevoke(t *testing.T) {
	fixture := newRemoteSessionFixture(t)
	userID := fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.RevokeUserSessions(context.Background(), userID, ""); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().RemoteSessionFeedDial(context.Background(), "shell-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (401 后重登)", got)
	}
}

func TestRemoteSessionFeedDialWithoutSessionIs401(t *testing.T) {
	fixture := newRemoteSessionFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client, err := newRemoteClient(RemoteConfig{URL: fixture.URL, Username: "alice", Password: "alice-pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.dialRemoteWS(context.Background(), "/share/remote-sessions/shell-1/feed", "远程会话推流端点"); !errors.Is(err, errSessionExpired) {
		t.Fatalf("dial without login = %v, want errSessionExpired", err)
	}
}

func TestRemoteSessionFeedDialEmptyID(t *testing.T) {
	fixture := newRemoteSessionFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Remote().RemoteSessionFeedDial(context.Background(), ""); err == nil {
		t.Fatal("empty session id must fail")
	}
}
