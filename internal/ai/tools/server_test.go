package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestSearchAndDockerArgumentsCannotReachShell(t *testing.T) {
	files := newFakeFS()
	registry, transport := fileRegistry(files)
	result := executeTool(registry, "j", "search_files", `{"path":"/tmp; touch /tmp/pwn","pattern":"*","by":"name"}`)
	if !result.OK {
		t.Fatal(result.Text)
	}
	if transport.calls() != 0 {
		t.Fatal("search used shell")
	}
	if len(files.listCalls) != 1 || files.listCalls[0] != "/tmp; touch /tmp/pwn" {
		t.Fatalf("path was not literal: %v", files.listCalls)
	}
	var dockerCalls atomic.Int64
	registry = NewRegistry(Dependencies{DockerLogs: func(context.Context, string, string, int, string) (string, error) {
		dockerCalls.Add(1)
		return "", nil
	}})
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "d", Name: "docker_logs", Args: json.RawMessage(`{"container_id":"web; touch /tmp/pwn"}`)}, nil)
	if result.OK || dockerCalls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d", result, dockerCalls.Load())
	}
}

func TestWaitForCancellation(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "working", Tail: []string{"working"}}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan Output, 1)
	go func() {
		finished <- registry.Execute(ctx, "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"done","timeout_ms":120000}`)}, nil)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case result := <-finished:
		if result.OK {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("wait_for ignored cancellation")
	}
}

func TestInvalidRegexReturnsImmediately(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{}})
	result := registry.Execute(context.Background(), "j", Scope{TabID: "t"}, Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"[","timeout_ms":120000}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "正则无效") {
		t.Fatalf("result=%+v", result)
	}
}

func TestCrossScopeTabRejected(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{}, TabSession: func(string) string { return "other" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if result.OK || !strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecCancellation(t *testing.T) {
	transport := &fakeTransport{files: newFakeFS(), block: true}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan Output, 1)
	go func() {
		finished <- registry.Execute(ctx, "j", Scope{SessionID: "s"}, Call{ID: "e", Name: "exec_commands", Args: json.RawMessage(`{"commands":["sleep 100"]}`)}, nil)
	}()
	for transport.calls() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case result := <-finished:
		if result.OK {
			t.Fatal("cancelled exec succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("exec ignored cancellation")
	}
}

func TestAuditRedactionAndDBTruncation(t *testing.T) {
	var audits []AuditEntry
	transport := &fakeTransport{files: newFakeFS(), result: base.ExecResult{Stdout: "ok"}}
	registry := NewRegistry(Dependencies{
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		Audit:     func(_ context.Context, entry AuditEntry) error { audits = append(audits, entry); return nil },
	})
	result := executeTool(registry, "j", "exec_commands", `{"commands":["curl --password secret https://example.test"]}`)
	if !result.OK || len(audits) != 1 || strings.Contains(fmt.Sprint(audits[0].Payload), "secret") {
		t.Fatalf("result=%+v audits=%+v", result, audits)
	}

	rows := make([][]any, 51)
	for i := range rows {
		rows[i] = []any{i}
	}
	registry = NewRegistry(Dependencies{Database: &fakeDatabase{result: db.QueryResult{Columns: []string{"n"}, Rows: rows}}})
	result = registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: "q", Name: "db_query", Args: json.RawMessage(`{"sql":"SELECT n FROM t"}`)}, nil)
	if !result.OK || !result.Truncated || strings.Count(result.Text, "\n") < 50 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTerminalCreateToolExecution(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{"title":"构建机","command":"make -j4"}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if len(terminal.createCalls) != 1 || terminal.createCalls[0].Title != "构建机" || terminal.createCalls[0].Command != "make -j4" {
		t.Fatalf("createCalls=%v", terminal.createCalls)
	}
	if !strings.Contains(result.Text, "tab_id=tab-new") {
		t.Fatalf("result must report the new tab id: %q", result.Text)
	}
}

func TestTerminalCreateDescriptionDocumentsCommandKinds(t *testing.T) {
	description := einoDescriptions["terminal_create"]
	for _, want := range []string{"ssh/docker/local/device", "send_keys", "standard input"} {
		if !strings.Contains(description, want) {
			t.Fatalf("terminal_create description missing %q: %q", want, description)
		}
	}
}

func TestTerminalCreateForegroundOpensTab(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "s" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{"title":"构建机"}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if result.Terminal == nil || result.Terminal.TabID != "tab-new" || result.Terminal.SessionID != "s" || result.Terminal.Title != "构建机" {
		t.Fatalf("terminal payload = %+v", result.Terminal)
	}
	if !strings.Contains(result.Text, "已创建并在前台展示") {
		t.Fatalf("text=%q", result.Text)
	}
	read := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"tab_id":"tab-new"}`)}, nil)
	if !read.OK {
		t.Fatalf("read_screen on created tab: %+v", read)
	}
	keys := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "k", Name: "send_keys", Args: json.RawMessage(`{"keys":"ls","tab_id":"tab-new"}`)}, nil)
	if !keys.OK || len(terminal.writes) != 1 {
		t.Fatalf("send_keys on created tab: %+v writes=%d", keys, len(terminal.writes))
	}
	crossJob := registry.Execute(context.Background(), "j2", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r2", Name: "read_screen", Args: json.RawMessage(`{"tab_id":"tab-new"}`)}, nil)
	if crossJob.OK || !strings.Contains(crossJob.Text, ErrCrossScope.Error()) {
		t.Fatalf("cross-job read=%+v", crossJob)
	}
	other := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "other" }})
	if created := other.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{}`)}, nil); !created.OK {
		t.Fatalf("create=%+v", created)
	}
	crossSession := other.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r3", Name: "read_screen", Args: json.RawMessage(`{"tab_id":"tab-new"}`)}, nil)
	if crossSession.OK || !strings.Contains(crossSession.Text, ErrCrossScope.Error()) {
		t.Fatalf("cross-session read=%+v", crossSession)
	}
}

func TestTerminalCreateBackgroundStaysDetached(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "s" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{"background":true}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if result.Terminal != nil {
		t.Fatalf("background create must not carry a foreground payload: %+v", result.Terminal)
	}
	if !strings.Contains(result.Text, "分离终端") || !strings.Contains(result.Text, "「后台会话」") {
		t.Fatalf("text=%q", result.Text)
	}
	read := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"tab_id":"tab-new"}`)}, nil)
	if !read.OK {
		t.Fatalf("read_screen on background tab: %+v", read)
	}
}

func TestTerminalCreateRequiresSessionScope(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "j", Scope{}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{}`)}, nil)
	if result.OK || len(terminal.createCalls) != 0 {
		t.Fatalf("result=%+v", result)
	}
	registry = NewRegistry(Dependencies{})
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "c", Name: "terminal_create", Args: json.RawMessage(`{}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "终端服务未配置") {
		t.Fatalf("result=%+v", result)
	}
}

func TestTerminalRenameToolExecution(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "s" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "terminal_rename", Args: json.RawMessage(`{"title":"部署终端"}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if len(terminal.renameCalls) != 1 || terminal.renameCalls[0] != [2]string{"t", "部署终端"} {
		t.Fatalf("renameCalls=%v", terminal.renameCalls)
	}
}

func TestTerminalRenameCrossScopeRejected(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "s" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "terminal_rename", Args: json.RawMessage(`{"tab_id":"other","title":"x"}`)}, nil)
	if result.OK || !strings.Contains(result.Text, ErrCrossScope.Error()) || len(terminal.renameCalls) != 0 {
		t.Fatalf("result=%+v", result)
	}
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "r", Name: "terminal_rename", Args: json.RawMessage(`{"title":"x"}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "tabId") {
		t.Fatalf("missing scope tab: result=%+v", result)
	}
	if result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "terminal_rename", Args: json.RawMessage(`{}`)}, nil); result.OK {
		t.Fatal("missing title must fail validation")
	}
}

func TestSessionDependenciesEnforceAccountOwnership(t *testing.T) {
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &fakeTransport{files: newFakeFS()}, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	owner := ipc.WithUserID(context.Background(), "owner")
	other := ipc.WithUserID(context.Background(), "other")
	connected, err := manager.Connect(owner, session.Asset{ID: "owner-ssh", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.RegisterRemoteSession(owner, session.RemoteSessionOptions{OwnerID: "owner", AssetID: "owner-asset", Title: "owner-tab"})
	if err != nil {
		t.Fatal(err)
	}
	deps := WithSession(Dependencies{}, manager)
	if _, err := deps.Transport(owner, connected.ID); err != nil {
		t.Fatalf("owner transport = %v", err)
	}
	if _, err := deps.Transport(other, connected.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account transport = %v, want ErrForbidden", err)
	}
	if _, err := deps.Terminal.Snapshot(owner, tab.ID); err != nil {
		t.Fatalf("owner snapshot = %v", err)
	}
	if _, err := deps.Terminal.Snapshot(other, tab.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account snapshot = %v, want ErrForbidden", err)
	}
	commands := deps.Terminal.(CommandStateTerminal)
	if _, err := commands.CommandState(other, tab.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account command state = %v, want ErrForbidden", err)
	}
	renames := deps.Terminal.(RenameTerminal)
	if err := renames.Rename(other, tab.ID, "foreign"); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account rename = %v, want ErrForbidden", err)
	}
	if err := renames.Rename(owner, tab.ID, "owner-renamed"); err != nil {
		t.Fatalf("owner rename = %v", err)
	}
	assets := deps.Terminal.(SessionAssetTerminal)
	if assetID, err := assets.SessionAssetID(other, connected.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account asset %q = %v, want ErrForbidden", assetID, err)
	}

	legacyTabSession := deps.TabSession
	var legacyCalls atomic.Int64
	deps.TabSession = func(tabID string) string {
		legacyCalls.Add(1)
		return legacyTabSession(tabID)
	}
	registry := NewRegistry(deps)
	scope := Scope{SessionID: tab.SessionID, TabID: tab.ID}
	if err := manager.FeedRemote(owner, tab.ID, []byte("owner-only-marker")); err != nil {
		t.Fatal(err)
	}
	t.Run("read screen rejects before legacy tab lookup", func(t *testing.T) {
		legacyCalls.Store(0)
		ownerResult := registry.Execute(owner, "owner", scope, Call{ID: "owner-read", Name: "read_screen"}, nil)
		if !ownerResult.OK || !strings.Contains(ownerResult.Text, "owner-only-marker") {
			t.Fatalf("owner read_screen = %+v", ownerResult)
		}
		otherResult := registry.Execute(other, "other", scope, Call{ID: "other-read", Name: "read_screen"}, nil)
		if otherResult.OK || !strings.Contains(otherResult.Text, session.ErrForbidden.Error()) || strings.Contains(otherResult.Text, "owner-only-marker") {
			t.Fatalf("cross-account read_screen = %+v", otherResult)
		}
		openResult := registry.Execute(context.Background(), "open", scope, Call{ID: "open-read", Name: "read_screen"}, nil)
		if !openResult.OK || !strings.Contains(openResult.Text, "owner-only-marker") {
			t.Fatalf("trusted open read_screen = %+v", openResult)
		}
		if calls := legacyCalls.Load(); calls != 0 {
			t.Fatalf("request-aware tab resolution fell back to unscoped TabSession %d times", calls)
		}
	})
	t.Run("grant identity rejects before legacy tab lookup", func(t *testing.T) {
		legacyCalls.Store(0)
		if assetID := ResolveGrantDeviceID(owner, registry, Scope{TabID: tab.ID}); assetID != "owner-asset" {
			t.Fatalf("owner grant device = %q", assetID)
		}
		if assetID := ResolveGrantDeviceID(other, registry, Scope{TabID: tab.ID}); assetID != "" {
			t.Fatalf("cross-account grant device = %q", assetID)
		}
		if assetID := ResolveGrantDeviceID(context.Background(), registry, Scope{TabID: tab.ID}); assetID != "owner-asset" {
			t.Fatalf("trusted open grant device = %q", assetID)
		}
		if calls := legacyCalls.Load(); calls != 0 {
			t.Fatalf("request-aware grant resolution fell back to unscoped TabSession %d times", calls)
		}
	})
}
