package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// tabFakeTerminal is a multi-tab terminal fake implementing every optional
// capability the P1 terminal tools rely on.
type tabFakeTerminal struct {
	mu               sync.Mutex
	tabs             map[string]*tabFakeTab
	writes           map[string][][]byte
	pastes           map[string][]string
	pasteSubmits     []pasteSubmitCall
	createResult     CreatedTab
	createErr        error
	created          []CreatedTab
	renamed          [][2]string
	sessionAssets    map[string]AssetIdentity
	snapshotErr      error
	snapshotCalls    int
	snapshotErrAfter int
	snapshotHook     func(tabID string)
	outputSinceHook  func(tabID string)
}

type tabFakeTab struct {
	sessionID string
	screen    Screen
	ring      *fakeRing
	command   CommandState
	detail    TabDetail
}

type pasteSubmitCall struct {
	tabID  string
	text   string
	submit bool
	delay  time.Duration
}

func newTabFake(tabs ...*tabFakeTab) *tabFakeTerminal {
	fake := &tabFakeTerminal{
		tabs:          make(map[string]*tabFakeTab, len(tabs)),
		writes:        make(map[string][][]byte),
		pastes:        make(map[string][]string),
		created:       []CreatedTab{},
		sessionAssets: make(map[string]AssetIdentity),
	}
	for _, tab := range tabs {
		fake.tabs[tab.detail.TabID] = tab
	}
	return fake
}

func (f *tabFakeTerminal) tab(tabID string) (*tabFakeTab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tab, ok := f.tabs[tabID]
	if !ok {
		return nil, fmt.Errorf("tab %s not found", tabID)
	}
	return tab, nil
}

func (f *tabFakeTerminal) Snapshot(_ context.Context, tabID string) (Screen, error) {
	f.mu.Lock()
	f.snapshotCalls++
	if f.snapshotErr != nil && f.snapshotCalls > f.snapshotErrAfter {
		f.mu.Unlock()
		return Screen{}, f.snapshotErr
	}
	hook := f.snapshotHook
	f.mu.Unlock()
	if hook != nil {
		hook(tabID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tab, ok := f.tabs[tabID]
	if !ok {
		return Screen{}, fmt.Errorf("tab %s not found", tabID)
	}
	return tab.screen, nil
}

func (f *tabFakeTerminal) Write(_ context.Context, tabID string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[tabID] = append(f.writes[tabID], append([]byte(nil), data...))
	return nil
}

func (f *tabFakeTerminal) WritePaste(_ context.Context, tabID, text string) error {
	f.mu.Lock()
	f.pastes[tabID] = append(f.pastes[tabID], text)
	f.mu.Unlock()
	return f.Write(context.Background(), tabID, terminal.EncodePaste(text, false))
}

// WritePasteSubmit records the combined paste+delay+Enter submission; tests
// assert send_keys uses it instead of split writes.
func (f *tabFakeTerminal) WritePasteSubmit(_ context.Context, tabID, text string, submit bool, delay time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pasteSubmits = append(f.pasteSubmits, pasteSubmitCall{tabID: tabID, text: text, submit: submit, delay: delay})
	return nil
}

func (f *tabFakeTerminal) WaitSnapshot(ctx context.Context, tabID string) (Screen, error) {
	return f.Snapshot(ctx, tabID)
}

func (f *tabFakeTerminal) TabSession(_ context.Context, tabID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tab, ok := f.tabs[tabID]
	if !ok {
		return "", session.ErrTabNotFound
	}
	return tab.sessionID, nil
}

func (f *tabFakeTerminal) CommandState(_ context.Context, tabID string) (CommandState, error) {
	tab, err := f.tab(tabID)
	if err != nil {
		return CommandState{}, err
	}
	return tab.command, nil
}

func (f *tabFakeTerminal) OutputSince(_ context.Context, tabID string, seq uint64, maxBytes int) ([]byte, uint64, uint64, error) {
	tab, err := f.tab(tabID)
	if err != nil {
		return nil, 0, 0, err
	}
	// Complete the read before firing the hook: a hook that lets a producer
	// append in response must never have its output swallowed by this read's
	// fromSeq — the append is new output for the next scan, not this one.
	data, start, latest, err := tab.ring.since(seq, maxBytes)
	if err != nil {
		return nil, 0, 0, err
	}
	f.mu.Lock()
	hook := f.outputSinceHook
	f.mu.Unlock()
	if hook != nil {
		hook(tabID)
	}
	return data, start, latest, nil
}

func (f *tabFakeTerminal) ListTerminalTabs(context.Context) ([]TerminalBrief, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	briefs := make([]TerminalBrief, 0, len(f.tabs))
	for _, tab := range f.tabs {
		briefs = append(briefs, TerminalBrief{
			Handle:    TerminalHandle{TabID: tab.detail.TabID, SessionID: tab.sessionID}.String(),
			TabID:     tab.detail.TabID,
			SessionID: tab.sessionID,
			AssetID:   "asset-" + tab.sessionID,
			Kind:      tab.detail.Kind,
			Title:     tab.detail.Title,
			CWD:       tab.detail.CWD,
			Durable:   tab.detail.Durable,
			Exited:    tab.detail.Exited,
		})
	}
	return briefs, nil
}

func (f *tabFakeTerminal) TabDetail(_ context.Context, tabID string) (TabDetail, error) {
	tab, err := f.tab(tabID)
	if err != nil {
		return TabDetail{}, err
	}
	return tab.detail, nil
}

func (f *tabFakeTerminal) Create(_ context.Context, sessionID string, options CreateTerminalOptions) (CreatedTab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return CreatedTab{}, f.createErr
	}
	created := f.createResult
	if created.TabID == "" {
		created = CreatedTab{TabID: "tab-new", Title: options.Title}
	}
	f.created = append(f.created, created)
	return created, nil
}

// bindSessionAsset registers the asset identity a session is bound to, so
// tests can exercise the execution-time identity re-validation.
func (f *tabFakeTerminal) bindSessionAsset(sessionID string, identity AssetIdentity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionAssets[sessionID] = identity
}

// SessionAssetIdentity reports the identity bound via bindSessionAsset; a
// session without a registered binding reports no asset, mirroring a local
// session in production.
func (f *tabFakeTerminal) SessionAssetIdentity(_ context.Context, sessionID string) (AssetIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionAssets[sessionID], nil
}

func (f *tabFakeTerminal) Rename(_ context.Context, tabID, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renamed = append(f.renamed, [2]string{tabID, title})
	return nil
}

func (f *tabFakeTerminal) writeCount(tabID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes[tabID])
}

func fakeTab(tabID, sessionID, text string, seq uint64) *tabFakeTab {
	return &tabFakeTab{
		sessionID: sessionID,
		screen:    Screen{Text: text, CursorRow: 0, CursorCol: len([]rune(text)), Cols: 80, Rows: 24, Seq: seq},
		ring:      newFakeRing(seq, ""),
		detail:    TabDetail{TabID: tabID, SessionID: sessionID},
	}
}

func p1Registry(terminal Terminal) *Registry {
	return NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
}

func p1Call(id, name, args string) Call {
	return Call{ID: id, Name: name, Args: json.RawMessage(args)}
}

// P1.2: handles resolve to the exact tab and distinguish gone from replaced.
func TestTerminalHandleResolveGoneAndReplaced(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r", "read_screen", `{"tab_id":"t/t1/s1"}`), nil)
	if !result.OK {
		t.Fatalf("handle resolve failed: %+v", result)
	}

	// The tab vanished: absence verified against the live tab set.
	fake.mu.Lock()
	delete(fake.tabs, "t1")
	fake.mu.Unlock()
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", `{"tab_id":"t/t1/s1"}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("gone handle = %+v", result)
	}

	// The ID now belongs to another session: replaced, never silently reused.
	fake.mu.Lock()
	fake.tabs["t1"] = fakeTab("t1", "s2", "$ ", 0)
	fake.mu.Unlock()
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("r3", "read_screen", `{"tab_id":"t/t1/s1"}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalReplaced.Error()) {
		t.Fatalf("replaced handle = %+v", result)
	}

	// A handle for another session never widens the scope.
	fake.mu.Lock()
	fake.tabs["t9"] = fakeTab("t9", "s9", "$ ", 0)
	fake.mu.Unlock()
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("r4", "read_screen", `{"tab_id":"t/t9/s9"}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("cross-session handle = %+v", result)
	}
}

func TestTerminalHandleRejectsMalformed(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	for _, value := range []string{"t/", "t/only", "x/t1/s1", "t//s1", "t/t1/"} {
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("r", "read_screen", fmt.Sprintf(`{"tab_id":%q}`, value)), nil)
		if result.OK {
			t.Fatalf("malformed handle %q accepted", value)
		}
	}
	if _, err := ParseTerminalHandle("t1"); err == nil {
		t.Fatal("plain tab ID parsed as handle")
	}
}

// P1.2/P1.3: terminal_list pages the full roster with stable ordering and
// exact identities.
func TestTerminalListPagination(t *testing.T) {
	fake := newTabFake(
		fakeTab("t3", "s1", "$ ", 0),
		fakeTab("t1", "s1", "$ ", 0),
		fakeTab("t5", "s2", "$ ", 0),
		fakeTab("t2", "s1", "$ ", 0),
		fakeTab("t4", "s1", "$ ", 0),
	)
	fake.tabs["t2"].detail = TabDetail{TabID: "t2", SessionID: "s1", Kind: "ssh", Title: "build", CWD: "/srv/app", Durable: true}
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}

	var seen []string
	cursor := ""
	pages := 0
	for {
		args := `{"limit":2}`
		if cursor != "" {
			args = fmt.Sprintf(`{"limit":2,"cursor":%q}`, cursor)
		}
		result := registry.Execute(context.Background(), "job", scope, p1Call("l", "terminal_list", args), nil)
		if !result.OK {
			t.Fatalf("page %d: %+v", pages, result)
		}
		pages++
		if len(result.Terminals) == 0 {
			t.Fatalf("empty page without termination: %q", result.Text)
		}
		for _, brief := range result.Terminals {
			seen = append(seen, brief.TabID)
			if brief.Handle != "t/"+brief.TabID+"/"+brief.SessionID {
				t.Fatalf("brief handle mismatch: %+v", brief)
			}
		}
		if !strings.Contains(result.Text, "共 5 个终端标签页") {
			t.Fatalf("total missing: %q", result.Text)
		}
		match := regexp.MustCompile(`下一页 cursor=(\S+)`).FindStringSubmatch(result.Text)
		if match == nil {
			break
		}
		cursor = match[1]
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
	want := []string{"t1", "t2", "t3", "t4", "t5"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("pagination order = %v, want %v", seen, want)
	}
}

func TestTerminalListBriefCarriesIdentity(t *testing.T) {
	fake := newTabFake(fakeTab("t2", "s1", "$ ", 0))
	fake.tabs["t2"].detail = TabDetail{TabID: "t2", SessionID: "s1", Kind: "ssh", Title: "build", CWD: "/srv/app", Durable: true}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t2"},
		p1Call("l", "terminal_list", `{}`), nil)
	if !result.OK {
		t.Fatalf("list = %+v", result)
	}
	for _, want := range []string{`title="build"`, `cwd="/srv/app"`, "kind=ssh", "durable", "t/t2/s1"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("list text missing %q: %q", want, result.Text)
		}
	}
	if len(result.Terminals) != 1 || result.Terminals[0].Kind != "ssh" || result.Terminals[0].CWD != "/srv/app" {
		t.Fatalf("structured briefs = %+v", result.Terminals)
	}
}

func TestTerminalListValidationAndCapability(t *testing.T) {
	registry := p1Registry(&fakeTerminal{})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("l", "terminal_list", `{}`), nil)
	if result.OK || !strings.Contains(result.Text, "不支持列出标签页") {
		t.Fatalf("missing capability = %+v", result)
	}
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry = p1Registry(fake)
	for _, args := range []string{`{"limit":201}`, `{"limit":-1}`} {
		result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("l", "terminal_list", args), nil)
		if result.OK {
			t.Fatalf("args %s accepted", args)
		}
	}
	if !PlanAllowed("terminal_list") {
		t.Fatal("terminal_list must stay plan-mode readable")
	}
}

// P1.2: read_screen carries the tab identity header when the terminal
// exposes it and degrades silently when not.
func TestReadScreenIdentityHeader(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.tabs["t1"].detail = TabDetail{TabID: "t1", SessionID: "s1", Kind: "ssh", Title: "deploy", CWD: "/srv/app"}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("r", "read_screen", `{}`), nil)
	if !result.OK {
		t.Fatalf("read_screen = %+v", result)
	}
	for _, want := range []string{"终端「deploy」", "kind=ssh", `cwd="/srv/app"`, "handle=t/t1/s1"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("header missing %q: %q", want, result.Text)
		}
	}

	plain := p1Registry(&fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24}})
	result = plain.Execute(context.Background(), "job", Scope{TabID: "t"},
		p1Call("r", "read_screen", `{}`), nil)
	if !result.OK || strings.Contains(result.Text, "handle=") {
		t.Fatalf("plain terminal header = %+v", result)
	}
}

// P1.3: terminal_create returns the handle and a deterministic operation ID,
// and connects assets only on explicit opt-in with visible failure stages
// and residual-resource contracts.
func TestTerminalCreateHandleAndOperationID(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	first := registry.Execute(context.Background(), "job", scope,
		p1Call("c1", "terminal_create", `{"title":"build"}`), nil)
	if !first.OK || first.Terminal == nil {
		t.Fatalf("create = %+v", first)
	}
	if first.Terminal.Handle != "t/tab-new/s1" {
		t.Fatalf("handle = %q", first.Terminal.Handle)
	}
	if !strings.Contains(first.Text, "handle=t/tab-new/s1") {
		t.Fatalf("text missing handle: %q", first.Text)
	}
	if first.OperationID == "" {
		t.Fatal("operation ID missing")
	}
	again := registry.Execute(context.Background(), "job", scope,
		p1Call("c1", "terminal_create", `{"title":"build"}`), nil)
	if again.OperationID != first.OperationID {
		t.Fatalf("operation ID not deterministic: %q vs %q", again.OperationID, first.OperationID)
	}
	other := registry.Execute(context.Background(), "job", scope,
		p1Call("c2", "terminal_create", `{"title":"build"}`), nil)
	if other.OperationID == first.OperationID {
		t.Fatal("operation ID collides across calls")
	}
}

type fakeConnector struct {
	sessionID string
	err       error
	calls     int
}

func (c *fakeConnector) ConnectAsset(context.Context, string) (string, error) {
	c.calls++
	return c.sessionID, c.err
}

func TestTerminalCreateConnectStages(t *testing.T) {
	resolveErr := fmt.Errorf("资产未连接: %w", ErrNoLiveSession)
	newTabFake := func() *tabFakeTerminal { return newTabFake(fakeTab("t1", "s1", "$ ", 0)) }

	t.Run("no opt-in keeps resolve error", func(t *testing.T) {
		connector := &fakeConnector{sessionID: "s9"}
		registry := p1Registry(newTabFake())
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolveErr }
		registry.deps.AssetConnector = connector
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1"}`), nil)
		if result.OK || !strings.Contains(result.Text, resolveErr.Error()) {
			t.Fatalf("result = %+v", result)
		}
		if connector.calls != 0 {
			t.Fatal("connector called without opt-in")
		}
	})

	t.Run("missing connector is explicit", func(t *testing.T) {
		registry := p1Registry(newTabFake())
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolveErr }
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if result.OK || !strings.Contains(result.Text, "未配置资产连接建立") || !strings.Contains(result.Text, resolveErr.Error()) {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("connect failure names both stages", func(t *testing.T) {
		connectErr := errors.New("dial timeout")
		registry := p1Registry(newTabFake())
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolveErr }
		registry.deps.AssetConnector = &fakeConnector{err: connectErr}
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if result.OK || !strings.Contains(result.Text, "解析资产会话失败") || !strings.Contains(result.Text, "建立新连接失败") ||
			!strings.Contains(result.Text, resolveErr.Error()) || !strings.Contains(result.Text, connectErr.Error()) {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("create failure after connect names residual session", func(t *testing.T) {
		fake := newTabFake()
		fake.createErr = errors.New("shell 启动失败")
		fake.bindSessionAsset("s9", AssetIdentity{AssetID: "a1", ConfigFingerprint: "fp-a1"})
		registry := p1Registry(fake)
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolveErr }
		registry.deps.AssetConnector = &fakeConnector{sessionID: "s9"}
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if result.OK || !strings.Contains(result.Text, "残留资源") || !strings.Contains(result.Text, "session_id=s9") {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("connect then create succeeds", func(t *testing.T) {
		fake := newTabFake()
		fake.bindSessionAsset("s9", AssetIdentity{AssetID: "a1", ConfigFingerprint: "fp-a1"})
		registry := p1Registry(fake)
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolveErr }
		registry.deps.AssetConnector = &fakeConnector{sessionID: "s9"}
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if !result.OK || !strings.Contains(result.Text, "已建立新连接 session_id=s9") {
			t.Fatalf("result = %+v", result)
		}
		if result.Terminal == nil || result.Terminal.Handle != "t/tab-new/s9" {
			t.Fatalf("terminal = %+v", result.Terminal)
		}
		if result.OperationID == "" {
			t.Fatal("operation ID missing for connected create")
		}
	})

	t.Run("live session skips connector", func(t *testing.T) {
		connector := &fakeConnector{sessionID: "s9"}
		registry := p1Registry(newTabFake())
		registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s-live", nil }
		registry.deps.AssetConnector = connector
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
		if !result.OK || connector.calls != 0 || strings.Contains(result.Text, "已建立新连接") {
			t.Fatalf("result = %+v calls=%d", result, connector.calls)
		}
	})
}

// P1.4: multi-tab wait with any/all modes, lines condition, and per-tab
// result metadata carrying exact anchors.
// waitAnchoredHook returns a hook plus per-tab channels that close when the
// wait has read each tab's anchor, so tests append output strictly after
// anchoring instead of racing a sleep. The hook fires only after the
// triggering read completes, so an append made in response is new output for
// the next scan — never swallowed by the anchored read's fromSeq.
func waitAnchoredHook(tabIDs ...string) (func(string), map[string]chan struct{}) {
	anchored := make(map[string]chan struct{}, len(tabIDs))
	onces := make(map[string]*sync.Once, len(tabIDs))
	for _, tabID := range tabIDs {
		anchored[tabID] = make(chan struct{})
		onces[tabID] = &sync.Once{}
	}
	hook := func(tabID string) {
		if once, ok := onces[tabID]; ok {
			once.Do(func() { close(anchored[tabID]) })
		}
	}
	return hook, anchored
}

// The anchor hook fires only after the triggering read completes: output
// appended in response to it is new output for the next scan, never
// swallowed by the anchored read's fromSeq. This pins the hook-after-read
// interleaving deterministically — with the hook firing before the read,
// the append below would land inside the read and fail the assertion.
func TestTabFakeOutputSinceHookFiresAfterRead(t *testing.T) {
	tab := fakeTab("t1", "s1", "$ ", 5)
	fake := newTabFake(tab)
	var once sync.Once
	fake.outputSinceHook = func(string) {
		once.Do(func() { tab.ring.append("after-anchor\r\n") })
	}
	data, _, latest, err := fake.OutputSince(context.Background(), "t1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "after-anchor") {
		t.Fatalf("read included output appended in the hook: %q", data)
	}
	// The append is new output for the follow-up scan.
	data2, _, latest2, err := fake.OutputSince(context.Background(), "t1", latest, 100)
	if err != nil || !strings.Contains(string(data2), "after-anchor") || latest2 <= latest {
		t.Fatalf("follow-up read = %q latest=%d err=%v", data2, latest2, err)
	}
}

func TestWaitForTabsAnyMode(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 10)
	t2 := fakeTab("t2", "s1", "$ ", 20)
	fake := newTabFake(t1, t2)
	hook, anchored := waitAnchoredHook("t2")
	fake.outputSinceHook = hook
	registry := p1Registry(fake)
	resultCh := make(chan Output, 1)
	go func() {
		resultCh <- registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"pattern":"done","mode":"any","timeout_ms":2000}`), nil)
	}()
	<-anchored["t2"]
	t2.ring.append("done\r\n")
	result := <-resultCh
	if !result.OK || result.Wait == nil {
		t.Fatalf("wait = %+v", result)
	}
	if !result.Wait.Satisfied || result.Wait.TimedOut || result.Wait.Mode != "any" {
		t.Fatalf("outcome = %+v", result.Wait)
	}
	if len(result.Wait.Tabs) != 2 {
		t.Fatalf("tabs = %+v", result.Wait.Tabs)
	}
	byID := map[string]WaitTabOutcome{}
	for _, tab := range result.Wait.Tabs {
		byID[tab.TabID] = tab
	}
	if !byID["t2"].Matched || byID["t1"].Matched {
		t.Fatalf("per-tab matched = %+v", byID)
	}
	if byID["t2"].FromSeq != 20 || byID["t2"].ToSeq <= 20 {
		t.Fatalf("t2 anchors = %+v", byID["t2"])
	}
	if byID["t1"].FromSeq != 10 || byID["t1"].ToSeq != 10 {
		t.Fatalf("t1 anchors = %+v", byID["t1"])
	}
	if !strings.Contains(result.Text, "已满足等待条件") {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestWaitForTabsAllModeTimeout(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 10)
	t2 := fakeTab("t2", "s1", "$ ", 20)
	fake := newTabFake(t1, t2)
	hook, anchored := waitAnchoredHook("t2")
	fake.outputSinceHook = hook
	registry := p1Registry(fake)
	resultCh := make(chan Output, 1)
	go func() {
		resultCh <- registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"pattern":"ready","mode":"all","timeout_ms":80}`), nil)
	}()
	<-anchored["t2"]
	t2.ring.append("ready\r\n")
	result := <-resultCh
	if result.OK || result.ExitCode != 1 || result.Wait == nil {
		t.Fatalf("wait = %+v", result)
	}
	if result.Wait.Satisfied || !result.Wait.TimedOut {
		t.Fatalf("outcome = %+v", result.Wait)
	}
	matched := 0
	for _, tab := range result.Wait.Tabs {
		if tab.Matched {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("matched = %d, want 1: %+v", matched, result.Wait.Tabs)
	}
	if !strings.Contains(result.Text, "等待超时") || !strings.Contains(result.Text, "1/2") {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestWaitForTabsQuietOnly(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 50
	t2.screen.IdleMS = 80
	fake := newTabFake(t1, t2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":10,"timeout_ms":2000}`), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("quiet-only wait = %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if !tab.Matched {
			t.Fatalf("tab %s not quiet: %+v", tab.TabID, tab)
		}
	}

	t1.screen.IdleMS = 5
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w2", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":10,"timeout_ms":40}`), nil)
	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("noisy quiet-only wait = %+v", result)
	}
}

func TestWaitForLinesCountsOnlyNewOutput(t *testing.T) {
	tab := fakeTab("t1", "s1", "$ ", 0)
	tab.ring = newFakeRing(0, "old-a\r\nold-b\r\n")
	fake := newTabFake(tab)
	registry := p1Registry(fake)
	// Anchor after the old content: lines above the anchor never count.
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", fmt.Sprintf(`{"lines":2,"since_seq":%d,"timeout_ms":80}`, tab.ring.latest)), nil)
	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("old output must not satisfy lines: %+v", result)
	}
	tab.ring.append("new-a\r\nnew-b\r\nnew-c\r\n")
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w2", "wait_for", fmt.Sprintf(`{"lines":2,"since_seq":%d,"timeout_ms":2000}`, 12)), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("lines wait = %+v", result)
	}
	if result.Wait.Tabs[0].NewLines < 2 {
		t.Fatalf("new_lines = %+v", result.Wait.Tabs[0])
	}
	if result.Wait.Tabs[0].FromSeq != 12 {
		t.Fatalf("anchor = %+v", result.Wait.Tabs[0])
	}
}

func TestWaitForMultiValidation(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t2", "s1", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	for name, args := range map[string]string{
		"since_seq with multi": `{"tab_ids":["t1","t2"],"pattern":"x","since_seq":3}`,
		"tab_id with tab_ids":  `{"tab_id":"t1","tab_ids":["t2"],"pattern":"x"}`,
		"bad mode":             `{"tab_ids":["t1","t2"],"pattern":"x","mode":"bogus"}`,
		"negative lines":       `{"lines":-1}`,
		"no condition":         `{}`,
	} {
		result := registry.Execute(context.Background(), "job", scope, p1Call("w", "wait_for", args), nil)
		if result.OK {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestWaitForSingleTabPopulatesMetadata(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ready\r\n$ ", Cols: 80, Rows: 24, Seq: 7}}
	registry := p1Registry(terminal)
	result := registry.Execute(context.Background(), "job", Scope{TabID: "t"},
		p1Call("w", "wait_for", `{"pattern":"ready","timeout_ms":2000}`), nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("wait = %+v", result)
	}
	if len(result.Wait.Tabs) != 1 || !result.Wait.Tabs[0].Matched || result.Wait.Tabs[0].ToSeq != 7 {
		t.Fatalf("metadata = %+v", result.Wait)
	}
}

// P1.5: paste literal guards.
func TestSendKeysPasteLiteralGuards(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"line1\r\nline2","paste":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "回车符") {
		t.Fatalf("carriage return accepted: %+v", result)
	}

	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"a\u0007b","paste":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "控制字符") {
		t.Fatalf("control byte accepted: %+v", result)
	}

	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"a\u0007b","paste":true,"allow_control":true}`), nil)
	if !result.OK {
		t.Fatalf("allow_control rejected: %+v", result)
	}

	for _, args := range []string{
		`{"keys":"ls","submit_delay_ms":50}`,
		`{"keys":"ls","paste":true,"submit_delay_ms":-1}`,
		`{"keys":"ls","paste":true,"submit_delay_ms":10001}`,
	} {
		result = registry.Execute(context.Background(), "job", scope, p1Call("k", "send_keys", args), nil)
		if result.OK {
			t.Fatalf("args %s accepted", args)
		}
	}
}

func TestSendKeysPasteLiteralNote(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", scopeS1(),
		p1Call("k", "send_keys", `{"keys":"echo <enter>","paste":true}`), nil)
	if !result.OK {
		t.Fatalf("paste = %+v", result)
	}
	if !strings.Contains(result.Text, "按字面发送") || !strings.Contains(result.Notes, "按字面发送") {
		t.Fatalf("literal note missing: %+v", result)
	}
}

func TestDisplaySendKeysPasteShowsLiteralText(t *testing.T) {
	display := DisplaySendKeys(Call{Name: "send_keys", Args: json.RawMessage(`{"keys":"echo <enter>","paste":true,"enter":true}`)}, "", 0)
	if !strings.Contains(display, "paste") || !strings.Contains(display, `"echo <enter>"`) {
		t.Fatalf("paste display = %q", display)
	}
	if strings.HasSuffix(display, "<enter> <enter>") {
		t.Fatalf("literal text rendered as actions: %q", display)
	}
}

func scopeS1() Scope { return Scope{SessionID: "s1", TabID: "t1"} }

// P1.5: the same write lock covers paste, delay, and Enter when the terminal
// supports it; the fallback split is always explicit.
func TestSendKeysPasteSubmitUsesCombinedLock(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", scopeS1(),
		p1Call("k", "send_keys", `{"keys":"make build","paste":true,"enter":true,"submit_delay_ms":250}`), nil)
	if !result.OK {
		t.Fatalf("paste submit = %+v", result)
	}
	if len(fake.pasteSubmits) != 1 {
		t.Fatalf("pasteSubmits = %+v", fake.pasteSubmits)
	}
	call := fake.pasteSubmits[0]
	if call.text != "make build" || !call.submit || call.delay != 250*time.Millisecond {
		t.Fatalf("submit call = %+v", call)
	}
	if fake.writeCount("t1") != 0 {
		t.Fatalf("split writes leaked: %+v", fake.writes["t1"])
	}
	if strings.Contains(result.Text, "分次写入") {
		t.Fatalf("unexpected split note: %q", result.Text)
	}
}

func TestSendKeysPasteSubmitDefaultDelay(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", scopeS1(),
		p1Call("k", "send_keys", `{"keys":"ls","paste":true,"enter":true}`), nil)
	if !result.OK {
		t.Fatalf("paste submit = %+v", result)
	}
	if len(fake.pasteSubmits) != 1 || fake.pasteSubmits[0].delay != defaultSubmitDelayMS*time.Millisecond {
		t.Fatalf("submit call = %+v", fake.pasteSubmits)
	}
}

// splitPasteTerminal lacks PasteSubmitTerminal, forcing the split path.
type splitPasteTerminal struct {
	fakeTerminal
}

func TestSendKeysPasteSplitFallbackIsExplicit(t *testing.T) {
	terminal := &splitPasteTerminal{fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24}}}
	registry := p1Registry(terminal)
	result := registry.Execute(context.Background(), "job", Scope{TabID: "t"},
		p1Call("k", "send_keys", `{"keys":"ls","paste":true,"enter":true}`), nil)
	if !result.OK {
		t.Fatalf("split paste = %+v", result)
	}
	if len(terminal.writes) != 2 {
		t.Fatalf("writes = %q", terminal.writes)
	}
	if !strings.Contains(result.Text, "分次写入") || !strings.Contains(result.Notes, "separate writes") {
		t.Fatalf("split note missing: %+v", result)
	}
	if !strings.Contains(result.Text, "operation_id") && result.OperationID == "" {
		t.Fatalf("operation ID missing: %+v", result)
	}
}

// P1.7: user-input arbitration.
func TestSendKeysRefusesUnseenUserInput(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	if result := registry.Execute(context.Background(), "job", scope, p1Call("r", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("baseline read = %+v", result)
	}
	// The user types after the AI last looked.
	terminal.mu.Lock()
	terminal.screen = Screen{Text: "$ rm -rf /tmp/x", CursorCol: 14, Cols: 80, Rows: 24}
	terminal.mu.Unlock()

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"ls","enter":true}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrUserInputPending.Error()) {
		t.Fatalf("arbitration = %+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("write went through: %q", terminal.writes)
	}

	// force=true writes with an explicit note.
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k2", "send_keys", `{"keys":"ls","enter":true,"force":true}`), nil)
	if !result.OK || !strings.Contains(result.Text, "force=true") {
		t.Fatalf("forced write = %+v", result)
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("forced writes = %q", terminal.writes)
	}

	// A fresh read_screen re-baselines and the next write proceeds without
	// force.
	if result := registry.Execute(context.Background(), "job", scope, p1Call("r2", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("rebaseline read = %+v", result)
	}
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k3", "send_keys", `{"keys":"ls","enter":true}`), nil)
	if !result.OK {
		t.Fatalf("post-read write refused: %+v", result)
	}
	if len(terminal.writes) != 2 {
		t.Fatalf("writes = %q", terminal.writes)
	}
}

func TestSendKeysOwnWritesDoNotTripArbitration(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}
	for index := 0; index < 3; index++ {
		result := registry.Execute(context.Background(), "job", scope,
			p1Call(fmt.Sprintf("k%d", index), "send_keys", `{"keys":"true","enter":true}`), nil)
		if !result.OK {
			t.Fatalf("write %d refused: %+v", index, result)
		}
	}
	if len(terminal.writes) != 3 {
		t.Fatalf("writes = %q", terminal.writes)
	}
}

func TestSendKeysArbitrationDegradesOnSnapshotFailure(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.snapshotErr = errors.New("快照不可用")
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", scopeS1(),
		p1Call("k", "send_keys", `{"keys":"ls","enter":true}`), nil)
	if !result.OK || !strings.Contains(result.Text, "无法校验") {
		t.Fatalf("degraded write = %+v", result)
	}
	if fake.writeCount("t1") != 1 {
		t.Fatalf("writes = %+v", fake.writes["t1"])
	}
}

// P1.6: geometry-bound cursors, deep history, unwrapped lines.
func TestReadScreenCursorRoundTrip(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	registry := p1Registry(anchoredTerminal(ring))
	scope := Scope{TabID: "tab"}
	first := registry.Execute(context.Background(), "job", scope,
		p1Call("r1", "read_screen", `{"since_seq":1,"max_bytes":4}`), nil)
	if !first.OK {
		t.Fatalf("page 1 = %+v", first)
	}
	match := regexp.MustCompile(`cursor=([A-Za-z0-9_-]+)`).FindStringSubmatch(first.Text)
	if match == nil {
		t.Fatalf("cursor missing: %q", first.Text)
	}
	second := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q,"max_bytes":64}`, match[1])), nil)
	if !second.OK {
		t.Fatalf("page 2 = %+v", second)
	}
	if !strings.Contains(second.Text, "新输出（序号 5 → 11") {
		t.Fatalf("page 2 header = %q", second.Text)
	}
	if strings.Contains(second.Text, "hello") {
		t.Fatalf("page 2 repeated page 1: %q", second.Text)
	}
}

func TestReadScreenCursorRejectedAfterResize(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	terminal := anchoredTerminal(ring)
	registry := p1Registry(terminal)
	scope := Scope{TabID: "tab"}
	first := registry.Execute(context.Background(), "job", scope,
		p1Call("r1", "read_screen", `{"since_seq":1,"max_bytes":4}`), nil)
	match := regexp.MustCompile(`cursor=([A-Za-z0-9_-]+)`).FindStringSubmatch(first.Text)
	if match == nil {
		t.Fatalf("cursor missing: %q", first.Text)
	}
	terminal.mu.Lock()
	terminal.screen.Cols = 100
	terminal.mu.Unlock()
	second := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, match[1])), nil)
	if second.OK || !strings.Contains(second.Text, "resize") || !strings.Contains(second.Text, "since_seq=5") {
		t.Fatalf("resize handling = %+v", second)
	}
}

func TestReadScreenCursorAndSinceSeqConflict(t *testing.T) {
	registry := p1Registry(anchoredTerminal(newFakeRing(0, "x")))
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		p1Call("r", "read_screen", `{"since_seq":1,"cursor":"abc"}`), nil)
	if result.OK {
		t.Fatalf("conflict accepted: %+v", result)
	}
}

func TestReadScreenHistory(t *testing.T) {
	ring := newFakeRing(0, strings.Repeat("history-line\r\n", 30)+"screen-line\r\n")
	terminal := anchoredTerminal(ring)
	terminal.screen.Rows = 24
	registry := p1Registry(terminal)
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		p1Call("r", "read_screen", `{"history_lines":3}`), nil)
	if !result.OK {
		t.Fatalf("history = %+v", result)
	}
	if !strings.Contains(result.Text, "历史 3 行") {
		t.Fatalf("history header = %q", result.Text)
	}
	if strings.Contains(result.Text, "screen-line") {
		t.Fatalf("screen leaked into history: %q", result.Text)
	}
	if !strings.Contains(result.Text, "since_seq=0") {
		t.Fatalf("earliest anchor missing: %q", result.Text)
	}

	// Evicted ring: the eviction is flagged, and the earliest retained anchor
	// is offered for forward paging.
	evicted := newFakeRing(100, "old\r\nnew\r\n")
	registry = p1Registry(anchoredTerminal(evicted))
	result = registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		p1Call("r", "read_screen", `{"history_lines":5}`), nil)
	if !result.OK || !strings.Contains(result.Text, "更早的输出已被淘汰") || !strings.Contains(result.Text, "since_seq=100") {
		t.Fatalf("evicted history = %+v", result)
	}
}

func TestUnwrapLines(t *testing.T) {
	wide := strings.Repeat("a", 80)
	joined := unwrapLines([]string{wide, "bc", "short"}, 80)
	if len(joined) != 2 || joined[0] != wide+"bc" || joined[1] != "short" {
		t.Fatalf("joined = %q", joined)
	}
	if got := unwrapLines([]string{"ab", "cd"}, 0); len(got) != 2 {
		t.Fatalf("cols=0 passthrough = %q", got)
	}
	// Exact-width lines join with their successor: inherent wrapped-content
	// ambiguity, documented in the tool description.
	joined = unwrapLines([]string{wide, "next"}, 80)
	if len(joined) != 1 {
		t.Fatalf("exact-width join = %q", joined)
	}
}

// Race coverage: the per-job input baseline and the multi-tab wait scanner
// run under -race with concurrent observers and producers.
func TestJobStateInputBaselineRace(t *testing.T) {
	state := newJobState()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 200; round++ {
				tabID := fmt.Sprintf("t%d", round%4)
				state.observeInput(tabID, fmt.Sprintf("buffer-%d-%d", worker, round), round)
				state.inputChanged(tabID, "other", round)
			}
		}(worker)
	}
	wg.Wait()
}

func TestWaitForTabsConcurrentOutputRace(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	fake := newTabFake(t1, t2)
	hook, anchored := waitAnchoredHook("t1", "t2")
	fake.outputSinceHook = hook
	registry := p1Registry(fake)
	var wg sync.WaitGroup
	resultCh := make(chan Output, 1)
	go func() {
		resultCh <- registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"lines":5,"mode":"all","timeout_ms":400}`), nil)
	}()
	<-anchored["t1"]
	<-anchored["t2"]
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 50; round++ {
			t1.ring.append("tick\r\n")
			t2.ring.append("tick\r\n")
			time.Sleep(time.Millisecond)
		}
	}()
	result := <-resultCh
	wg.Wait()
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("race wait = %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.NewLines < 5 {
			t.Fatalf("tab %s new_lines = %d", tab.TabID, tab.NewLines)
		}
	}
}
