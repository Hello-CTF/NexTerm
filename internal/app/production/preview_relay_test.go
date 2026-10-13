//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/session"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
	"github.com/coder/websocket"
)

// relayTestRemote is the in-memory implementation of previewRelayRemote: it records shell creation/link creation/deletion,
// and feed dialing is injected by tests via dialFeed with scripted fake connections.
type relayTestRemote struct {
	mu         sync.Mutex
	shells     map[string]bool
	deleted    []string
	links      int
	base       string
	createErr  error
	previewErr error
	deleteSig  chan string
}

func newRelayTestRemote() *relayTestRemote {
	return &relayTestRemote{shells: map[string]bool{}, base: "http://relay.test", deleteSig: make(chan string, 16)}
}

func (r *relayTestRemote) RemoteSessionCreate(_ context.Context, _ string, _ string, cols, rows uint32) (*syncservice.RemoteSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return nil, r.createErr
	}
	id := fmt.Sprintf("shell-%d", len(r.shells)+1)
	r.shells[id] = true
	return &syncservice.RemoteSession{SessionID: id, Cols: cols, Rows: rows}, nil
}

func (r *relayTestRemote) CreatePreviewLink(_ context.Context, sessionID string, _ int64) (*syncservice.RemotePreviewLinkCreate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.previewErr != nil {
		return nil, r.previewErr
	}
	r.links++
	return &syncservice.RemotePreviewLinkCreate{
		RemotePreviewLink: syncservice.RemotePreviewLink{ID: fmt.Sprintf("link-%d", r.links), SessionID: sessionID},
		Token:             "tok-" + sessionID,
	}, nil
}

func (r *relayTestRemote) RemoteSessionDelete(_ context.Context, id string) (syncservice.RemoteOK, error) {
	r.mu.Lock()
	delete(r.shells, id)
	r.deleted = append(r.deleted, id)
	r.mu.Unlock()
	r.deleteSig <- id
	return syncservice.RemoteOK{OK: true}, nil
}

func (r *relayTestRemote) RemoteSessionFeedDial(context.Context, string) (*websocket.Conn, error) {
	return nil, errors.New("unit tests inject previewRelayManager.dialFeed")
}

func (r *relayTestRemote) ServerBaseURL(context.Context) (string, error) {
	return r.base, nil
}

func (r *relayTestRemote) shellCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shells)
}

// relayTestFeed is a scripted fake feed connection: it records all written frames, and writes start failing after failAfter successes.
type relayTestFeed struct {
	mu        sync.Mutex
	writes    []relayTestWrite
	succeeded int
	failAfter int
	closeNow  bool
	changed   chan struct{}
}

type relayTestWrite struct {
	typ  websocket.MessageType
	data []byte
}

func newRelayTestFeed(failAfter int) *relayTestFeed {
	return &relayTestFeed{failAfter: failAfter, changed: make(chan struct{})}
}

func (f *relayTestFeed) signalLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *relayTestFeed) Write(_ context.Context, messageType websocket.MessageType, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeNow {
		return errors.New("feed closed")
	}
	if f.succeeded >= f.failAfter {
		return errors.New("scripted write failure")
	}
	f.succeeded++
	f.writes = append(f.writes, relayTestWrite{typ: messageType, data: append([]byte(nil), data...)})
	f.signalLocked()
	return nil
}

func (f *relayTestFeed) Close(websocket.StatusCode, string) error { return nil }

func (f *relayTestFeed) CloseNow() error {
	f.mu.Lock()
	f.closeNow = true
	f.signalLocked()
	f.mu.Unlock()
	return nil
}

// waitWrite waits for a write frame matching the condition to appear, returning its index (starting from from).
func (f *relayTestFeed) waitWrite(t *testing.T, from int, match func(relayTestWrite) bool) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		for index := from; index < len(f.writes); index++ {
			if match(f.writes[index]) {
				f.mu.Unlock()
				return index
			}
		}
		changed := f.changed
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for feed write (writes=%d)", len(f.writes))
		}
		select {
		case <-changed:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *relayTestFeed) writeAt(index int) relayTestWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[index]
}

func hasBinaryContaining(payload string) func(relayTestWrite) bool {
	return func(write relayTestWrite) bool {
		return write.typ == websocket.MessageBinary && strings.Contains(string(write.data), payload)
	}
}

func isEndFrame(write relayTestWrite) bool {
	if write.typ != websocket.MessageText {
		return false
	}
	var frame struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(write.data, &frame) == nil && frame.Type == "end"
}

// newRelayTestRig assembles the unit-test relay: a real session.Manager (local /bin/sh) + an in-memory
// remote surface; dialFeed is injected by each test with a scripted connection.
func newRelayTestRig(t *testing.T) (*previewRelayManager, *session.Manager, *relayTestRemote) {
	t.Helper()
	connector := session.ConnectorFunc(func(_ context.Context, _ session.Asset, _ uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
	sessions := session.NewManager(session.Config{Connector: connector})
	t.Cleanup(func() { _ = sessions.Close() })
	remote := newRelayTestRemote()
	manager := newPreviewRelayManager(sessions, remote, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	manager.pollInterval = 20 * time.Millisecond
	manager.redialBudget = time.Second
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, sessions, remote
}

func openRelayTestTab(t *testing.T, sessions *session.Manager) session.TabInfo {
	t.Helper()
	connected, err := sessions.Connect(context.Background(), session.Asset{
		ID: "relay-asset-" + ids.New(), Kind: session.KindLocal, Name: "relay-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := sessions.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "owner", ChannelID: "owner-" + ids.New(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// TestPreviewRelayLifecycle pins the relay's main path: create shell + link -> snapshot frame not forwarded -> incremental
// output binary frames flow upstream -> tab exits -> end control frame + shell delete -> Active cleared.
func TestPreviewRelayLifecycle(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	feed := newRelayTestFeed(1 << 30)
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return feed, nil }
	tab := openRelayTestTab(t, sessions)

	result, err := manager.Create(context.Background(), tab.ID, 3_600_000)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "shell-1" || result.Token != "tok-shell-1" {
		t.Fatalf("result = %+v", result)
	}
	if result.URL != "http://relay.test/share/preview/tok-shell-1" {
		t.Fatalf("url = %q", result.URL)
	}
	if active := manager.Active(tab.ID); active.SessionID != "shell-1" {
		t.Fatalf("active = %+v", active)
	}

	if err := sessions.Write(context.Background(), tab.ID, "owner", []byte("echo relay-unit-marker\n")); err != nil {
		t.Fatal(err)
	}
	index := feed.waitWrite(t, 0, hasBinaryContaining("relay-unit-marker"))
	for earlier := 0; earlier < index; earlier++ {
		if write := feed.writeAt(earlier); write.typ != websocket.MessageBinary {
			t.Fatalf("write #%d = %+v, 快照等 JSON 帧不得转发", earlier, write)
		}
	}

	if err := sessions.Write(context.Background(), tab.ID, "owner", []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	feed.waitWrite(t, index+1, isEndFrame)
	select {
	case deleted := <-remote.deleteSig:
		if deleted != "shell-1" {
			t.Fatalf("deleted = %q", deleted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for remote session delete")
	}
	if active := manager.Active(tab.ID); active.SessionID != "" {
		t.Fatalf("active after exit = %+v", active)
	}
}

// TestPreviewRelayDuplicateCreate pins the duplicate-create semantics: a single tab has only one active relay at a time,
// and repeated creates return the existing link directly without creating a new shell/link.
func TestPreviewRelayDuplicateCreate(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	feed := newRelayTestFeed(1 << 30)
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return feed, nil }
	tab := openRelayTestTab(t, sessions)

	first, err := manager.Create(context.Background(), tab.ID, 3_600_000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), tab.ID, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if *first != *second {
		t.Fatalf("duplicate create = %+v, want %+v", second, first)
	}
	if remote.shellCount() != 1 || remote.links != 1 {
		t.Fatalf("shells = %d links = %d, want 1/1", remote.shellCount(), remote.links)
	}
}

// TestPreviewRelayTabCloseDeletesShell pins the tab-close linkage: the subscription channel errors out on close,
// and the relay finishes on its own (end + shell delete + Active cleared).
func TestPreviewRelayTabCloseDeletesShell(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	feed := newRelayTestFeed(1 << 30)
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return feed, nil }
	tab := openRelayTestTab(t, sessions)
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err != nil {
		t.Fatal(err)
	}

	if err := sessions.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case deleted := <-remote.deleteSig:
		if deleted != "shell-1" {
			t.Fatalf("deleted = %q", deleted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for remote session delete")
	}
	if active := manager.Active(tab.ID); active.SessionID != "" {
		t.Fatalf("active after close = %+v", active)
	}
}

// TestPreviewRelayReconnect pins resume-after-disconnect: a write failure triggers backoff redial, frames not successfully
// written are resent to the new connection (no loss, no duplication), output keeps flowing upstream afterwards, and the
// finishing frame lands on the new connection.
func TestPreviewRelayReconnect(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	first := newRelayTestFeed(1) // writes start failing after the first frame succeeds
	second := newRelayTestFeed(1 << 30)
	var dials atomic.Int64
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) {
		if dials.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	tab := openRelayTestTab(t, sessions)
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err != nil {
		t.Fatal(err)
	}

	// Keep producing output until the redial happens: frames whose writes failed before the redial are resent to the new connection.
	deadline := time.Now().Add(10 * time.Second)
	for dials.Load() < 2 && time.Now().Before(deadline) {
		_ = sessions.Write(context.Background(), tab.ID, "owner", []byte("echo reconnect-marker\n"))
		time.Sleep(50 * time.Millisecond)
	}
	if dials.Load() < 2 {
		t.Fatal("feed write failure did not trigger redial")
	}
	_ = sessions.Write(context.Background(), tab.ID, "owner", []byte("echo after-reconnect\n"))
	second.waitWrite(t, 0, hasBinaryContaining("reconnect-marker"))
	second.waitWrite(t, 0, hasBinaryContaining("after-reconnect"))

	if err := sessions.Write(context.Background(), tab.ID, "owner", []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	second.waitWrite(t, 0, isEndFrame)
	select {
	case <-remote.deleteSig:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for remote session delete")
	}
}

// TestPreviewRelayRedialBudgetExhausted pins redial budget exhaustion: persistently failing to dial finishes the relay
// (shell delete + Active cleared), never letting backpressure block the local terminal long-term.
func TestPreviewRelayRedialBudgetExhausted(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	manager.redialBudget = 1200 * time.Millisecond
	first := newRelayTestFeed(0) // writes fail immediately
	manager.dialFeed = func() func(context.Context, string) (previewRelayFeed, error) {
		dialed := false
		return func(context.Context, string) (previewRelayFeed, error) {
			if !dialed {
				dialed = true
				return first, nil
			}
			return nil, errors.New("server unreachable")
		}
	}()
	tab := openRelayTestTab(t, sessions)
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err != nil {
		t.Fatal(err)
	}
	_ = sessions.Write(context.Background(), tab.ID, "owner", []byte("echo budget\n"))
	select {
	case deleted := <-remote.deleteSig:
		if deleted != "shell-1" {
			t.Fatalf("deleted = %q", deleted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for relay teardown after redial budget exhausted")
	}
	if active := manager.Active(tab.ID); active.SessionID != "" {
		t.Fatalf("active after give-up = %+v", active)
	}
}

// TestPreviewRelayCreateFailureCleansUpShell pins cleanup after any post-shell step fails: preview creation failure -> the shell
// must be deleted; dial failure -> the shell must be deleted.
func TestPreviewRelayCreateFailureCleansUpShell(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return nil, errors.New("dial refused") }
	tab := openRelayTestTab(t, sessions)
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err == nil {
		t.Fatal("dial failure must fail create")
	}
	select {
	case <-remote.deleteSig:
	case <-time.After(5 * time.Second):
		t.Fatal("shell must be deleted when dial fails")
	}
	if remote.shellCount() != 0 {
		t.Fatalf("shells = %d, want 0", remote.shellCount())
	}

	remote.previewErr = errors.New("preview refused")
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return newRelayTestFeed(1 << 30), nil }
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err == nil {
		t.Fatal("preview failure must fail create")
	}
	select {
	case <-remote.deleteSig:
	case <-time.After(5 * time.Second):
		t.Fatal("shell must be deleted when preview create fails")
	}
	if remote.shellCount() != 0 {
		t.Fatalf("shells = %d, want 0", remote.shellCount())
	}
}

// TestPreviewRelayExitedTabRejected pins create rejection for an exited terminal (no shell is produced).
func TestPreviewRelayExitedTabRejected(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	manager.dialFeed = func(context.Context, string) (previewRelayFeed, error) { return newRelayTestFeed(1 << 30), nil }
	tab := openRelayTestTab(t, sessions)
	if err := sessions.Write(context.Background(), tab.ID, "owner", []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tabState, err := sessions.Tab(tab.ID)
		if err == nil && tabState.Info().Exited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	tabState, err := sessions.Tab(tab.ID)
	if err != nil || !tabState.Info().Exited {
		t.Fatalf("tab exited = %v, %v", tabState.Info().Exited, err)
	}
	if _, err := manager.Create(context.Background(), tab.ID, 3_600_000); err == nil {
		t.Fatal("exited tab must be rejected")
	}
	if remote.shellCount() != 0 {
		t.Fatalf("shells = %d, want 0", remote.shellCount())
	}
}

// TestPreviewShareCommandArgsRejected pins preview_share_create/active argument validation: missing fields or empty nested args
// return bad_param without touching the relay (when the whole args envelope is missing, the input decodes to zero values and takes the same rejection).
func TestPreviewShareCommandArgsRejected(t *testing.T) {
	production := newUpdateTestProduction(t, true, nil)
	response := dispatchHostKeyTest(t, production, "preview_share_create", `{"args":{"tabId":"","ttlMs":0}}`)
	if response.OK || response.Error == nil || response.Error.Code != "bad_param" || response.Error.Message != "tabId/ttlMs 不能为空" {
		t.Fatalf("empty create args = %+v, want bad_param tabId/ttlMs", response.Error)
	}
	response = dispatchHostKeyTest(t, production, "preview_share_create", `{"tabId":"tab-1","ttlMs":3600000}`)
	if response.OK || response.Error == nil || response.Error.Code != "bad_param" {
		t.Fatalf("flat args = %+v, want bad_param (nested envelope required)", response.Error)
	}
	response = dispatchHostKeyTest(t, production, "preview_share_active", `{"args":{"tabId":""}}`)
	if response.OK || response.Error == nil || response.Error.Code != "bad_param" || response.Error.Message != "tabId 不能为空" {
		t.Fatalf("empty active args = %+v, want bad_param tabId", response.Error)
	}
}
