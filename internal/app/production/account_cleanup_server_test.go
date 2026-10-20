package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type accountCleanupHTTPSession struct {
	cookie *http.Cookie
	csrf   string
}

type accountCleanupHTTPResponse struct {
	status  int
	body    map[string]any
	cookies []*http.Cookie
}

type accountCleanupHTTPFixture struct {
	clock       *accountCleanupClock
	rig         *accountCleanupRig
	accounts    *account.Accounts
	db          *store.Store
	dispatcher  *ipc.Dispatcher
	http        *httptest.Server
	application *Production
}

func newAccountCleanupHTTPFixture(t *testing.T) *accountCleanupHTTPFixture {
	t.Helper()
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) {
		config.Now = clock.Now
	})
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	application := &Production{Services: ProductionServices{Sessions: rig.manager, Store: database}}
	dispatcher := ipc.NewDispatcher()
	accounts := account.New(database.GORM(), account.WithNow(clock.NowMS))
	api, err := server.New(server.Config{
		Options:    server.Options{Auth: server.AuthOn, Listen: "127.0.0.1:0"},
		Dispatcher: dispatcher,
		Accounts:   accounts,
		Channels: server.ChannelBinderFunc(func(string) (server.ChannelReceiver, error) {
			return nil, errors.New("channel not used by account cleanup test")
		}),
		AccountCleanup:        application.CleanupAccount,
		AccountCleanupPrepare: application.PrepareAccountCleanup,
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = api.Close()
	})
	return &accountCleanupHTTPFixture{clock: clock, rig: rig, accounts: accounts, db: database, dispatcher: dispatcher, http: httpServer, application: application}
}

func (f *accountCleanupHTTPFixture) call(t *testing.T, method, path string, body any, session *accountCleanupHTTPSession) accountCleanupHTTPResponse {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, f.http.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if session != nil {
		request.AddCookie(session.cookie)
		request.Header.Set("X-NexTerm-CSRF", session.csrf)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	decoded := make(map[string]any)
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
	}
	return accountCleanupHTTPResponse{status: response.StatusCode, body: decoded, cookies: response.Cookies()}
}

func (f *accountCleanupHTTPFixture) initUsers(t *testing.T) (string, string) {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	alice, err := f.accounts.InitSuperadmin(context.Background(), code, "alice", "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.accounts.CreateUser(context.Background(), "bob", "", "password-bob")
	if err != nil {
		t.Fatal(err)
	}
	return alice.ID, bob.ID
}

func (f *accountCleanupHTTPFixture) login(t *testing.T, username, password string) *accountCleanupHTTPSession {
	t.Helper()
	call := f.call(t, http.MethodPost, "/auth/login", map[string]any{"username": username, "password": password}, nil)
	if call.status != http.StatusOK {
		t.Fatalf("login status = %d body = %v", call.status, call.body)
	}
	csrf, _ := call.body["csrf_token"].(string)
	for _, cookie := range call.cookies {
		if cookie.Name == "nexterm_session" {
			return &accountCleanupHTTPSession{cookie: cookie, csrf: csrf}
		}
	}
	t.Fatal("login returned no account session cookie")
	return nil
}

func (f *accountCleanupHTTPFixture) expireOnlySession(t *testing.T, ownerID string) {
	t.Helper()
	sessions, err := f.accounts.ListUserSessions(context.Background(), ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("owner %s has %d active sessions, want 1", ownerID, len(sessions))
	}
	if err := f.db.GORM().Exec("UPDATE user_session SET expires_at = ? WHERE id = ?", f.clock.NowMS(), sessions[0].ID).Error; err != nil {
		t.Fatal(err)
	}
}

func TestAccountServerLogoutAllCleansDeviceOwnerWithoutFrontend(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID, bobID := fixture.initUsers(t)
	alice := fixture.login(t, "alice", "password-alice")
	bob := fixture.login(t, "bob", "password-bob")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	ownerB := ipc.WithUserID(t.Context(), bobID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})
	deviceB, tabB := fixture.rig.open(t, ownerB, session.Asset{ID: "device/b", Kind: session.KindDevice})

	call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, alice)
	if call.status != http.StatusOK {
		t.Fatalf("logout-all status = %d body = %v", call.status, call.body)
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, ownerB, fixture.rig.manager, tabB.ID, true)
	requireAccountCleanupStatus(t, ownerB, fixture.rig.manager, deviceB.ID, session.StatusConnected)
	if _, err := fixture.accounts.ValidateSession(context.Background(), bob.cookie.Value); err != nil {
		t.Fatalf("logout-all revoked another user's login session: %v", err)
	}
}

func TestAccountServerExpiredSessionUsesPreExpiryDeviceGeneration(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID, bobID := fixture.initUsers(t)
	alice := fixture.login(t, "alice", "password-alice")
	bob := fixture.login(t, "bob", "password-bob")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	ownerB := ipc.WithUserID(t.Context(), bobID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})
	deviceB, tabB := fixture.rig.open(t, ownerB, session.Asset{ID: "device/b", Kind: session.KindDevice})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if err := fixture.dispatcher.RegisterRaw("test_expired_device_cleanup", func(ctx context.Context, _ *ipc.Call) (any, error) {
		entered <- struct{}{}
		<-release
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan accountCleanupHTTPResponse, 2)
	for range 2 {
		go func() {
			callDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": "test_expired_device_cleanup", "args": map[string]any{},
			}, alice)
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("requests did not reach account session revalidation")
		}
	}
	fixture.clock.Advance(time.Millisecond)
	fixture.expireOnlySession(t, aliceID)
	fixture.clock.Advance(time.Millisecond)
	aliceS3 := fixture.login(t, "alice", "password-alice")
	deviceS3, tabS3 := fixture.rig.open(t, ownerA, session.Asset{ID: "device/s3", Kind: session.KindDevice})
	unblock()
	for range 2 {
		select {
		case call := <-callDone:
			if call.status != http.StatusOK || call.body["ok"] != false {
				t.Fatalf("expired session RPC status = %d body = %v", call.status, call.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("expired session RPC did not finish")
		}
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabS3.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceS3.ID, session.StatusConnected)
	requireAccountCleanupTab(t, ownerB, fixture.rig.manager, tabB.ID, true)
	requireAccountCleanupStatus(t, ownerB, fixture.rig.manager, deviceB.ID, session.StatusConnected)
	if got := fixture.rig.transport("device/a").channels[0].closeCount.Load(); got != 1 {
		t.Fatalf("expired device terminal close count = %d, want 1", got)
	}
	if got := fixture.rig.transport("device/s3").closeCount.Load(); got != 0 {
		t.Fatalf("S3 transport close count = %d, want 0", got)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), aliceS3.cookie.Value); err != nil {
		t.Fatalf("S3 login session was affected by S1 expiry cleanup: %v", err)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), bob.cookie.Value); err != nil {
		t.Fatalf("expiry cleanup revoked another user's login session: %v", err)
	}
}

func TestAccountServerExpiredSessionCleanupRetriesAfterSnapshotFailure(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID, _ := fixture.initUsers(t)
	alice := fixture.login(t, "alice", "password-alice")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var gateOnce [2]sync.Once
	releaseGate := func(index int) {
		gateOnce[index].Do(func() { close(gates[index]) })
	}
	t.Cleanup(func() {
		releaseGate(0)
		releaseGate(1)
	})
	entered := make(chan int, 2)
	var requestIndex atomic.Int32
	if err := fixture.dispatcher.RegisterRaw("test_expired_cleanup_retry", func(ctx context.Context, _ *ipc.Call) (any, error) {
		index := int(requestIndex.Add(1)) - 1
		entered <- index
		<-gates[index]
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan accountCleanupHTTPResponse, 2)
	for range 2 {
		go func() {
			callDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
				"cmd": "test_expired_cleanup_retry", "args": map[string]any{},
			}, alice)
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("requests did not reach account session revalidation")
		}
	}
	fixture.clock.Advance(time.Millisecond)
	fixture.expireOnlySession(t, aliceID)
	fixture.clock.Advance(time.Millisecond)

	// First revalidation: the snapshot read fails, so the deferred cleanup must
	// not touch the device and must leave the event retryable.
	fixture.application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("injected listing failure")
	}
	releaseGate(0)
	select {
	case call := <-callDone:
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("first expired session RPC status = %d body = %v", call.status, call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first expired session RPC did not finish")
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusConnected)

	// Second revalidation with the same event after the read recovers: the
	// retry takes a fresh snapshot and cleans the expired generation's device.
	fixture.application.accountCleanup.listOwnerSessions = nil
	releaseGate(1)
	select {
	case call := <-callDone:
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("second expired session RPC status = %d body = %v", call.status, call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second expired session RPC did not finish")
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
}
