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

// relayTestRemote 是 previewRelayRemote 的内存实现: 记录建壳/建链接/删壳,
// 推流拨号由测试经 dialFeed 注入脚本化假连接。
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

// relayTestFeed 是脚本化假推流连接: 记录全部写帧, failAfter 次成功后写开始失败。
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

// waitWrite 等待一个满足条件的写帧出现, 返回其下标 (从 from 起)。
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

// newRelayTestRig 装配单元测试中继: 真 session.Manager (本地 /bin/sh) + 内存
// 远程面; dialFeed 由各测试注入脚本化连接。
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

// TestPreviewRelayLifecycle 钉住中继主链路: 建壳+建链接 → 快照帧不转发 → 增量
// 输出二进制帧上行 → tab 退出 → end 控制帧 + 删壳 → Active 清空。
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

// TestPreviewRelayDuplicateCreate 钉住重复创建语义: 单 tab 同时只有一条活跃
// 中继, 重复创建直接返回既有链接, 不新建壳/链接。
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

// TestPreviewRelayTabCloseDeletesShell 钉住 tab 关闭联动: 订阅通道随关闭报错,
// 中继自行收尾 (end + 删壳 + Active 清空)。
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

// TestPreviewRelayReconnect 钉住断线续推: 写失败即按退避重拨, 未成功写出的帧
// 重发到新连接 (不丢不重), 之后输出继续上行, 收尾帧落在新连接上。
func TestPreviewRelayReconnect(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	first := newRelayTestFeed(1) // 第 1 帧成功后写开始失败
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

	// 持续制造输出直到重拨发生: 重拨前写失败的帧会重发到新连接。
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

// TestPreviewRelayRedialBudgetExhausted 钉住重拨预算耗尽: 持续拨不上即收尾
// (删壳 + Active 清空), 不让背压长期阻塞本地终端。
func TestPreviewRelayRedialBudgetExhausted(t *testing.T) {
	manager, sessions, remote := newRelayTestRig(t)
	manager.redialBudget = 1200 * time.Millisecond
	first := newRelayTestFeed(0) // 写入即失败
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

// TestPreviewRelayCreateFailureCleansUpShell 钉住建壳后任一步失败的回收: 预览
// 创建失败 → 壳必删; 拨号失败 → 壳必删。
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

// TestPreviewRelayExitedTabRejected 钉住已退出终端的创建拒绝 (不产生壳)。
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

func TestPreviewRelayCommandsRegistered(t *testing.T) {
	production := newUpdateTestProduction(t, false, nil)
	commands := production.Dispatcher.Commands()
	for _, command := range []string{"preview_share_create", "preview_share_active"} {
		found := false
		for _, registered := range commands {
			if registered == command {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production is missing %s", command)
		}
	}
}
