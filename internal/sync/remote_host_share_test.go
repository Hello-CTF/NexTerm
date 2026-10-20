package sync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

type hostShareWSFixture struct {
	*remoteAPIFixture
	recipientID string
	sawCSRF     atomic.Bool
}

func newHostShareWSFixture(t *testing.T) *hostShareWSFixture {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inner := &remoteAPIFixture{
		db:         db,
		accounts:   account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))),
		mfaTickets: map[string]string{},
	}
	fixture := &hostShareWSFixture{remoteAPIFixture: inner}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", inner.serveLogin)
	mux.HandleFunc("POST /auth/totp/login", inner.serveTOTPLogin)
	mux.Handle("GET /auth/me", inner.session(http.HandlerFunc(inner.serveMe)))
	mux.Handle("GET /auth/dek", inner.session(http.HandlerFunc(inner.serveDEK)))
	mux.Handle("POST /sync/v2/push", inner.session(inner.csrf(NewObjectHandler(db.GORM(), nil))))
	mux.Handle("POST /sync/v2/pull", inner.session(NewObjectHandler(db.GORM(), nil)))
	mux.Handle("POST /sync/v2/ids", inner.session(NewObjectHandler(db.GORM(), nil)))
	mux.Handle("GET /share/devices/{id}/terminal", inner.session(inner.csrf(http.HandlerFunc(fixture.serveTerminal))))
	inner.Server = httptest.NewServer(mux)
	t.Cleanup(inner.Server.Close)
	return fixture
}

func (f *hostShareWSFixture) serveTerminal(w http.ResponseWriter, r *http.Request) {
	f.sawCSRF.Store(true)
	if f.identity(r).UserID != f.recipientID {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "没有该设备的有效分享"))
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "share ended") }()
	if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"ready","session_id":"shared-session","permission":"read_write","expires_at":1790000000000}`)); err != nil {
		return
	}
	for {
		messageType, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if messageType != websocket.MessageBinary {
			continue
		}
		if err := conn.Write(r.Context(), websocket.MessageBinary, append([]byte("echo:"), payload...)); err != nil {
			return
		}
	}
}

func TestDialHostShareTerminalUsesRecipientSessionAndCSRF(t *testing.T) {
	fixture := newHostShareWSFixture(t)
	fixture.recipientID = fixture.createUser(t, "recipient", "recipient-pw-123")
	fixture.createUser(t, "stranger", "stranger-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "recipient", "recipient-pw-123")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := instance.service.Remote().DialHostShareTerminal(ctx, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	messageType, ready, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("ready = (%v, %s), %v", messageType, ready, err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("input")); err != nil {
		t.Fatal(err)
	}
	_, output, err := conn.Read(ctx)
	if err != nil || string(output) != "echo:input" {
		t.Fatalf("output = %q, %v", output, err)
	}
	if !fixture.sawCSRF.Load() {
		t.Fatal("dedicated share dial must carry cookie and CSRF")
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want cached account session reuse", got)
	}

	other := newTestInstance(t, true)
	if _, err := other.service.LinkSet(context.Background(), linkPatch(fixture.URL, "stranger", "stranger-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := other.service.Remote().DialHostShareTerminal(context.Background(), "device-1"); err == nil {
		t.Fatal("second account without a share must be rejected")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
			t.Fatalf("stranger error = %v, want forbidden", err)
		}
	}
}

func TestHostShareIdentityStableAcrossSyncAndSessionReplacement(t *testing.T) {
	fixture := newHostShareWSFixture(t)
	fixture.recipientID = fixture.createUser(t, "recipient", "recipient-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL+"/", "recipient", "recipient-pw-123")); err != nil {
		t.Fatal(err)
	}
	first, err := instance.service.Remote().HostShareIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.ServerURL != fixture.URL || first.UserID != fixture.recipientID || first.Generation == "" {
		t.Fatalf("identity = %+v", first)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := instance.service.Remote().DialHostShareTerminal(ctx, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := instance.service.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.VerifiedAt <= before.VerifiedAt {
		t.Fatalf("sync did not refresh VerifiedAt: before=%d after=%d", before.VerifiedAt, after.VerifiedAt)
	}
	second, err := instance.service.Remote().HostShareIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *second != *first {
		t.Fatalf("ordinary sync changed host-share identity: first=%+v second=%+v", first, second)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("ordinary sync replaced the login session: logins=%d", got)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("after-sync")); err != nil {
		t.Fatal(err)
	}
	_, output, err := conn.Read(ctx)
	if err != nil || string(output) != "echo:after-sync" {
		t.Fatalf("connection after sync = %q, %v", output, err)
	}

	if _, err := fixture.accounts.RevokeUserSessions(context.Background(), fixture.recipientID, ""); err != nil {
		t.Fatal(err)
	}
	replacement, err := instance.service.Remote().DialHostShareTerminal(ctx, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = replacement.CloseNow()
	third, err := instance.service.Remote().HostShareIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.Generation == first.Generation {
		t.Fatal("actual session replacement must change generation")
	}
}

func TestDialHostShareTerminalReLoginAfter401(t *testing.T) {
	fixture := newHostShareWSFixture(t)
	fixture.recipientID = fixture.createUser(t, "recipient", "recipient-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "recipient", "recipient-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.RevokeUserSessions(context.Background(), fixture.recipientID, ""); err != nil {
		t.Fatal(err)
	}
	conn, err := instance.service.Remote().DialHostShareTerminal(context.Background(), "device-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.CloseNow()
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want re-login after 401", got)
	}
}

func TestDialHostShareTerminalWithoutSessionIs401(t *testing.T) {
	fixture := newHostShareWSFixture(t)
	fixture.recipientID = fixture.createUser(t, "recipient", "recipient-pw-123")
	client, err := newRemoteClient(RemoteConfig{URL: fixture.URL, Username: "recipient", Password: "recipient-pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.dialHostShareTerminal(context.Background(), "device-1"); !errors.Is(err, errSessionExpired) {
		t.Fatalf("dial without session = %v, want session expired", err)
	}
}

func TestDialHostShareTerminalRejectsEmptyDevice(t *testing.T) {
	instance := newTestInstance(t, true)
	if _, err := instance.service.Remote().DialHostShareTerminal(context.Background(), ""); err == nil {
		t.Fatal("empty device id must fail")
	}
}
