package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

func TestSocketTrackerCloseSessionAndUser(t *testing.T) {
	var tracker socketTracker
	ctx := context.Background()
	identity := func(user, session, device string) *account.Identity {
		return &account.Identity{UserID: user, SessionID: session, DeviceID: device}
	}
	_, aRevoked, aRelease, err := tracker.trackAccount(ctx, socketKindEvents, identity("u1", "s1", "d1"))
	if err != nil {
		t.Fatal(err)
	}
	_, bRevoked, bRelease, _ := tracker.trackAccount(ctx, socketKindEvents, identity("u1", "s2", ""))
	_, cRevoked, cRelease, _ := tracker.trackAccount(ctx, socketKindEvents, identity("u2", "s3", "d1"))
	_, openRevoked, openRelease, _ := tracker.trackAccount(ctx, socketKindEvents, nil)
	defer openRelease()
	defer cRelease()
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}

	tracker.closeSession("s1")
	if !closed(aRevoked) || closed(bRevoked) || closed(cRevoked) || closed(openRevoked) {
		t.Fatal("closeSession must only signal the matching session")
	}
	tracker.closeUser("u1", "s2")
	if closed(bRevoked) {
		t.Fatal("closeUser must spare exceptSession")
	}
	tracker.closeDevice("u1", "d1")
	if closed(cRevoked) {
		t.Fatal("closeDevice must not cross users")
	}
	tracker.closeUser("u1", "")
	if !closed(bRevoked) || closed(cRevoked) || closed(openRevoked) {
		t.Fatal("closeUser must signal every session of that user only")
	}
	tracker.closeUser("", "")
	tracker.closeSession("")
	if closed(cRevoked) || closed(openRevoked) {
		t.Fatal("empty ids must not match open-access sockets")
	}
	tracker.closeDevice("u2", "d1")
	if !closed(cRevoked) {
		t.Fatal("closeDevice must signal the user's device sessions")
	}
	aRelease()
	bRelease()
	tracker.mu.Lock()
	remaining := len(tracker.entries)
	tracker.mu.Unlock()
	if remaining != 2 {
		t.Fatalf("released sockets must not linger, remaining=%d", remaining)
	}
}

func TestSocketTrackerRejectsAfterClose(t *testing.T) {
	var tracker socketTracker
	_, _, release, err := tracker.trackAccount(context.Background(), socketKindEvents, &account.Identity{UserID: "u", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- tracker.closeAndWait(ctx) }()
	waitFor(t, func() bool {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		return tracker.closed
	})
	if _, _, _, err := tracker.trackAccount(context.Background(), socketKindEvents, nil); err == nil {
		t.Fatal("track after close must fail")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ownedReplayHub lets /ws/channel admit account connections by naming each channel's owner.
type ownedReplayHub struct {
	*replayHub
	mu     sync.Mutex
	owners map[string]string
}

func (h *ownedReplayHub) ChannelOwner(id string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owners[id], nil
}

func (h *ownedReplayHub) setOwner(id, user string) {
	h.mu.Lock()
	h.owners[id] = user
	h.mu.Unlock()
}

type revocationEnv struct {
	fixture     *accountFixture
	hub         *ownedReplayHub
	sessions    map[string]*accountTestSession
	recoveryKey string
	deviceID    string
	users       map[string]string
}

type revocationSocket struct {
	label string
	kind  string
	conn  *websocket.Conn
	done  chan error
}

func newRevocationEnv(t *testing.T) *revocationEnv {
	t.Helper()
	hub := &ownedReplayHub{replayHub: newReplayHub(), owners: map[string]string{}}
	fixture := newAccountFixtureWithChannels(t, hub)
	root, recoveryKey := fixture.initSuperadmin(t, "root", "password-r1")
	for _, name := range []string{"bob", "carol"} {
		if call := fixture.call(t, http.MethodPost, "/admin/users", map[string]any{"username": name, "password": "password-" + name}, root, root.csrf, nil); call.status != http.StatusOK {
			t.Fatalf("create %s status=%d", name, call.status)
		}
	}
	device, err := fixture.accounts.RegisterDevice(context.Background(), root.user["id"].(string), "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	env := &revocationEnv{fixture: fixture, hub: hub, recoveryKey: recoveryKey, deviceID: device.ID, users: map[string]string{"root": root.user["id"].(string)}}
	bob := fixture.login(t, "bob", "password-bob")
	carol := fixture.login(t, "carol", "password-carol")
	env.users["bob"], env.users["carol"] = bob.user["id"].(string), carol.user["id"].(string)
	env.sessions = map[string]*accountTestSession{
		"root": root, "root2": fixture.login(t, "root", "password-r1"),
		"rootdev": loginWithDevice(t, fixture, "root", "password-r1", device.ID),
		"bob":     bob, "bob2": fixture.login(t, "bob", "password-bob"),
		"carol": carol,
	}
	return env
}

func ownerOf(label string) string { return strings.TrimRight(strings.TrimSuffix(label, "dev"), "2") }

func (e *revocationEnv) openSockets(t *testing.T, ctx context.Context) []*revocationSocket {
	t.Helper()
	base := strings.Replace(e.fixture.http.URL, "http", "ws", 1)
	var sockets []*revocationSocket
	for label, session := range e.sessions {
		e.hub.setOwner("ch-"+label, e.users[ownerOf(label)])
		for kind, path := range map[string]string{"events": "/ws/events", "channel": "/ws/channel/ch-" + label} {
			header := http.Header{}
			header.Add("Cookie", session.cookie.String())
			conn, _, err := websocket.Dial(ctx, base+path, &websocket.DialOptions{HTTPHeader: header})
			if err != nil {
				t.Fatalf("dial %s %s: %v", label, kind, err)
			}
			socket := &revocationSocket{label: label, kind: kind, conn: conn, done: make(chan error, 1)}
			go func() {
				_, _, err := conn.Read(context.Background())
				socket.done <- err
			}()
			sockets = append(sockets, socket)
			t.Cleanup(func() { _ = conn.CloseNow() })
		}
	}
	waitFor(t, func() bool {
		e.fixture.server.sockets.mu.Lock()
		defer e.fixture.server.sockets.mu.Unlock()
		return len(e.fixture.server.sockets.entries) == len(sockets)
	})
	return sockets
}

func TestAccountSocketsClosedOnRevocation(t *testing.T) {
	cases := []struct {
		name   string
		closed []string
		revoke func(t *testing.T, e *revocationEnv)
	}{
		{"password_change", []string{"bob2"}, func(t *testing.T, e *revocationEnv) {
			body, _, _ := envelopeBody(t, "password-new")
			body = mergeBody(body, map[string]any{"old_password": "password-bob", "new_password": "password-new"})
			e.expectOK(t, http.MethodPost, "/auth/password", body, "bob")
		}},
		{"admin_disable", []string{"bob", "bob2"}, func(t *testing.T, e *revocationEnv) {
			e.expectOK(t, http.MethodPost, "/admin/users/"+e.users["bob"]+"/disable", nil, "root")
		}},
		{"admin_reset", []string{"bob", "bob2"}, func(t *testing.T, e *revocationEnv) {
			e.expectOK(t, http.MethodPost, "/admin/users/"+e.users["bob"]+"/reset", nil, "root")
		}},
		{"recovery_reset", []string{"root", "root2", "rootdev"}, func(t *testing.T, e *revocationEnv) {
			body, _, _ := envelopeBody(t, "password-r2")
			body = mergeBody(body, map[string]any{"username": "root", "recovery_key": e.recoveryKey, "new_password": "password-r2"})
			e.expectOK(t, http.MethodPost, "/auth/recovery/reset", body, "")
		}},
		{"device_revoke", []string{"rootdev"}, func(t *testing.T, e *revocationEnv) {
			e.expectOK(t, http.MethodDelete, "/auth/devices/"+e.deviceID, nil, "root")
		}},
		{"logout", []string{"bob"}, func(t *testing.T, e *revocationEnv) {
			e.expectOK(t, http.MethodPost, "/auth/logout", nil, "bob")
		}},
		{"logout_all", []string{"bob", "bob2"}, func(t *testing.T, e *revocationEnv) {
			e.expectOK(t, http.MethodPost, "/auth/logout-all", nil, "bob")
		}},
		{"revoke_single_session", []string{"bob2"}, func(t *testing.T, e *revocationEnv) {
			id := bobOtherSessionID(t, e)
			e.expectOK(t, http.MethodDelete, "/auth/sessions/"+id, nil, "bob")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRevocationEnv(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			sockets := env.openSockets(t, ctx)
			tc.revoke(t, env)
			wantClosed := map[string]bool{}
			for _, label := range tc.closed {
				wantClosed[label] = true
			}
			for _, socket := range sockets {
				if !wantClosed[socket.label] {
					continue
				}
				select {
				case err := <-socket.done:
					if code := websocket.CloseStatus(err); code != websocket.StatusPolicyViolation {
						t.Errorf("%s %s closed with code %d (%v), want policy violation", socket.label, socket.kind, code, err)
					}
				case <-time.After(5 * time.Second):
					t.Errorf("%s %s was not closed after revocation", socket.label, socket.kind)
				}
			}
			for _, socket := range sockets {
				if wantClosed[socket.label] {
					continue
				}
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				err := socket.conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					t.Errorf("%s %s must stay open: %v", socket.label, socket.kind, err)
				}
				select {
				case err := <-socket.done:
					t.Errorf("%s %s was closed unexpectedly: %v", socket.label, socket.kind, err)
				default:
				}
			}
		})
	}
}

// bobOtherSessionID returns the id of a bob session other than the one bob lists from (bob), i.e. bob2.
func bobOtherSessionID(t *testing.T, e *revocationEnv) string {
	t.Helper()
	call := e.fixture.call(t, http.MethodGet, "/auth/sessions", nil, e.sessions["bob"], "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("session list status=%d", call.status)
	}
	for _, raw := range call.body["sessions"].([]any) {
		entry := raw.(map[string]any)
		if current, _ := entry["current"].(bool); !current {
			return entry["id"].(string)
		}
	}
	t.Fatal("no non-current session")
	return ""
}

func (e *revocationEnv) expectOK(t *testing.T, method, path string, body any, as string) {
	t.Helper()
	var session *accountTestSession
	csrf := ""
	if as != "" {
		session = e.sessions[as]
		csrf = session.csrf
	}
	if call := e.fixture.call(t, method, path, body, session, csrf, nil); call.status != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%v", method, path, call.status, call.body)
	}
}

func TestAccountSocketsRevalidatedWithoutRevocationRoute(t *testing.T) {
	env := newRevocationEnv(t)
	env.fixture.server.accountSocketRevalidateNanos.Store(int64(25 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sockets := env.openSockets(t, ctx)
	// Invalidate through the account layer only, bypassing every HTTP revocation route.
	if err := env.fixture.accounts.RevokeUserSession(ctx, env.users["bob"], bobOtherSessionID(t, env)); err != nil {
		t.Fatal(err)
	}
	for _, socket := range sockets {
		if socket.label == "bob2" {
			select {
			case err := <-socket.done:
				if code := websocket.CloseStatus(err); code != websocket.StatusPolicyViolation {
					t.Errorf("%s %s close code %d (%v)", socket.label, socket.kind, code, err)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("%s %s survived the periodic recheck", socket.label, socket.kind)
			}
		}
	}
	for _, socket := range sockets {
		if socket.label == "bob" {
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := socket.conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				t.Errorf("valid session %s closed by recheck: %v", socket.kind, err)
			}
		}
	}
}

func newAccountFixtureWithChannels(t *testing.T, channels ChannelBinder) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthOn
	config.Channels = channels
	accounts := account.New(database.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key")))
	config.Accounts = accounts
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{server: server, http: httpServer, accounts: accounts, db: database.DB(), client: &http.Client{}}
}
