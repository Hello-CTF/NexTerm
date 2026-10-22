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
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
	batches     *execbatch.Service
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
	batches := execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(ctx context.Context, sessionID string) (execbatch.Target, error) {
			connected, err := rig.manager.SessionFor(ctx, sessionID)
			if err != nil {
				return execbatch.Target{}, err
			}
			transport, err := rig.manager.TransportFor(ctx, sessionID)
			if err != nil {
				return execbatch.Target{}, err
			}
			return execbatch.Target{SessionID: connected.ID, SessionName: connected.ID, Transport: transport, Generation: transport.Generation()}, nil
		}),
		Now: clock.Now,
	})
	application := &Production{Services: ProductionServices{Sessions: rig.manager, Store: database, ExecBatches: batches}}
	application.accountCleanup.now = clock.Now
	dispatcher := ipc.NewDispatcher()
	if err := ipc.Register(dispatcher, "exec_batch_execute", func(ctx context.Context, _ *ipc.Call, input execbatch.Request) (execbatch.Batch, error) {
		return batches.Execute(ctx, ipc.OwnerID(ctx), input)
	}); err != nil {
		t.Fatal(err)
	}
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
	return &accountCleanupHTTPFixture{clock: clock, rig: rig, accounts: accounts, db: database, dispatcher: dispatcher, http: httpServer, application: application, batches: batches}
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
	aliceID := f.initAlice(t)
	bob, err := f.accounts.CreateUser(context.Background(), "bob", "", "password-bob")
	if err != nil {
		t.Fatal(err)
	}
	return aliceID, bob.ID
}

func (f *accountCleanupHTTPFixture) initAlice(t *testing.T) string {
	t.Helper()
	code, err := f.accounts.GenerateInitCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	alice, err := f.accounts.InitSuperadmin(context.Background(), code, "alice", "password-alice")
	if err != nil {
		t.Fatal(err)
	}
	return alice.ID
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
	aliceID := fixture.initAlice(t)
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

func TestAccountServerLogoutAbortsWhenCleanupSnapshotFails(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID := fixture.initAlice(t)
	alice := fixture.login(t, "alice", "password-alice")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	fixture.application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("injected listing failure")
	}
	call := fixture.call(t, http.MethodPost, "/auth/logout", nil, alice)
	if call.status != http.StatusInternalServerError {
		t.Fatalf("logout with a failed cleanup snapshot status = %d body = %v, want 500", call.status, call.body)
	}
	// The abort must not revoke the session and must not touch the device.
	if _, err := fixture.accounts.ValidateSession(context.Background(), alice.cookie.Value); err != nil {
		t.Fatalf("logout aborted but the session was revoked: %v", err)
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusConnected)

	// The same event retries once the read recovers: full logout and cleanup.
	fixture.application.accountCleanup.listOwnerSessions = nil
	call = fixture.call(t, http.MethodPost, "/auth/logout", nil, alice)
	if call.status != http.StatusOK {
		t.Fatalf("logout retry status = %d body = %v", call.status, call.body)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), alice.cookie.Value); err == nil {
		t.Fatal("logout retry did not revoke the session")
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
}

func TestAccountServerLogoutAllAbortsWhenCleanupSnapshotFails(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID := fixture.initAlice(t)
	alice := fixture.login(t, "alice", "password-alice")
	alice2 := fixture.login(t, "alice", "password-alice")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	fixture.application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("injected listing failure")
	}
	call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, alice)
	if call.status != http.StatusInternalServerError {
		t.Fatalf("logout-all with a failed cleanup snapshot status = %d body = %v, want 500", call.status, call.body)
	}
	// The abort must not revoke any session and must not touch the device.
	if _, err := fixture.accounts.ValidateSession(context.Background(), alice.cookie.Value); err != nil {
		t.Fatalf("logout-all aborted but alice's session was revoked: %v", err)
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusConnected)

	fixture.application.accountCleanup.listOwnerSessions = nil
	call = fixture.call(t, http.MethodPost, "/auth/logout-all", nil, alice)
	if call.status != http.StatusOK {
		t.Fatalf("logout-all retry status = %d body = %v", call.status, call.body)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), alice.cookie.Value); err == nil {
		t.Fatal("logout-all retry did not revoke alice's session")
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), alice2.cookie.Value); err == nil {
		t.Fatal("logout-all retry did not revoke alice's second session")
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
}

func TestAccountServerSessionRevokeAbortsWhenCleanupSnapshotFails(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID := fixture.initAlice(t)
	alice := fixture.login(t, "alice", "password-alice")
	other := fixture.login(t, "alice", "password-alice")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	otherID := currentCleanupSessionID(t, fixture, other)

	fixture.application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("injected listing failure")
	}
	call := fixture.call(t, http.MethodDelete, "/auth/sessions/"+otherID, nil, alice)
	if call.status != http.StatusInternalServerError {
		t.Fatalf("session revoke with a failed cleanup snapshot status = %d body = %v, want 500", call.status, call.body)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), other.cookie.Value); err != nil {
		t.Fatalf("session revoke aborted but the target session was revoked: %v", err)
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusConnected)

	fixture.application.accountCleanup.listOwnerSessions = nil
	call = fixture.call(t, http.MethodDelete, "/auth/sessions/"+otherID, nil, alice)
	if call.status != http.StatusOK {
		t.Fatalf("session revoke retry status = %d body = %v", call.status, call.body)
	}
	if _, err := fixture.accounts.ValidateSession(context.Background(), other.cookie.Value); err == nil {
		t.Fatal("session revoke retry did not revoke the target session")
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)
}

func currentCleanupSessionID(t *testing.T, fixture *accountCleanupHTTPFixture, session *accountCleanupHTTPSession) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fixture.http.URL+"/auth/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(session.cookie)
	response, err := fixture.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	for _, row := range decoded.Sessions {
		if row.Current {
			return row.ID
		}
	}
	t.Fatal("current session not found")
	return ""
}

// Full-stack deterministic counterexample for the post-eviction hazard: a
// late revalidation of a long-revoked session must derive its terminal no-op
// from the durable revoked session row, not cancel the new generation's
// batch or device started after the retention TTL evicted the event.
func TestAccountServerPostEvictionLateRevalidationKeepsNewGeneration(t *testing.T) {
	fixture := newAccountCleanupHTTPFixture(t)
	aliceID := fixture.initAlice(t)
	alice := fixture.login(t, "alice", "password-alice")
	ownerA := ipc.WithUserID(t.Context(), aliceID)
	deviceA, tabA := fixture.rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	// Admit one request with S1 and park it inside the handler: its
	// revalidation fires the deferred cleanup once S1 is revoked.
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if err := fixture.dispatcher.RegisterRaw("test_late_revalidation_cleanup", func(ctx context.Context, _ *ipc.Call) (any, error) {
		close(entered)
		<-release
		return nil, ipc.CheckAccountSession(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	parkedDone := make(chan accountCleanupHTTPResponse, 1)
	go func() {
		parkedDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd": "test_late_revalidation_cleanup", "args": map[string]any{},
		}, alice)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach the parked handler")
	}

	// The paired logout-all commits the cleanup: the old device generation is
	// closed and the cleanup event is retained as a replay marker.
	if call := fixture.call(t, http.MethodPost, "/auth/logout-all", nil, alice); call.status != http.StatusOK {
		t.Fatalf("logout-all status = %d body = %v", call.status, call.body)
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabA.ID, false)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceA.ID, session.StatusDisconnected)

	// Past the retention TTL the committed event and its aliases are purged;
	// a probe cleanup applies the lazy purge before the late revalidation
	// (no batches are running yet, so its snapshot is empty).
	fixture.clock.Advance(accountCleanupEventTTL + time.Millisecond)
	if err := fixture.application.CleanupAccount(context.Background(), aliceID, "login-probe"); err != nil {
		t.Fatal(err)
	}

	// New generation: a fresh login, device, and blocking batch.
	alice2 := fixture.login(t, "alice", "password-alice")
	deviceNew, tabNew := fixture.rig.open(t, ownerA, session.Asset{ID: "device/new", Kind: session.KindDevice})
	batchStarted := make(chan struct{}, 1)
	fixture.rig.setExecHook("asset-ssh", func(ctx context.Context) (base.ExecResult, error) {
		batchStarted <- struct{}{}
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	sshSession, _ := fixture.rig.open(t, ownerA, session.Asset{ID: "asset-ssh", Kind: session.KindSSH})
	batchDone := make(chan accountCleanupHTTPResponse, 1)
	go func() {
		batchDone <- fixture.call(t, http.MethodPost, "/rpc", map[string]any{
			"cmd":  "exec_batch_execute",
			"args": execbatch.Request{BatchID: "new-batch", SessionIDs: []string{sshSession.ID}, Command: "wait"},
		}, alice2)
	}()
	select {
	case <-batchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new generation batch did not start")
	}

	// The parked S1 request revalidates as revoked: the deferred cleanup runs
	// against a purged event and must stay on the terminal generation.
	unblock()
	select {
	case call := <-parkedDone:
		if call.status != http.StatusOK || call.body["ok"] != false {
			t.Fatalf("parked revalidation status = %d body = %v", call.status, call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked revalidation did not finish")
	}
	select {
	case call := <-batchDone:
		t.Fatalf("late revalidation canceled the new generation batch: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
	requireAccountCleanupTab(t, ownerA, fixture.rig.manager, tabNew.ID, true)
	requireAccountCleanupStatus(t, ownerA, fixture.rig.manager, deviceNew.ID, session.StatusConnected)

	if _, err := fixture.batches.Cancel(t.Context(), aliceID, "new-batch"); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-batchDone:
		batch := decodeAccountCleanupExecBatch(t, call)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("new generation batch results = %+v, want canceled", batch.Results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new generation batch did not finish")
	}
}

func decodeAccountCleanupExecBatch(t *testing.T, call accountCleanupHTTPResponse) execbatch.Batch {
	t.Helper()
	if call.status != http.StatusOK || call.body["ok"] != true {
		t.Fatalf("execute RPC status = %d body = %v", call.status, call.body)
	}
	encoded, err := json.Marshal(call.body["data"])
	if err != nil {
		t.Fatal(err)
	}
	var batch execbatch.Batch
	if err := json.Unmarshal(encoded, &batch); err != nil {
		t.Fatal(err)
	}
	return batch
}
