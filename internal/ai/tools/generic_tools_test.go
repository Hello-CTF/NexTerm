package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedSnapshotTerminal struct {
	fakeTerminal
	screensMu sync.Mutex
	screens   []Screen
}

func (s *scriptedSnapshotTerminal) Snapshot(ctx context.Context, tabID string) (Screen, error) {
	s.screensMu.Lock()
	if len(s.screens) == 0 {
		s.screensMu.Unlock()
		return s.fakeTerminal.Snapshot(ctx, tabID)
	}
	screen := s.screens[0]
	s.screens = s.screens[1:]
	s.screensMu.Unlock()
	return screen, nil
}

func (s *scriptedSnapshotTerminal) WaitSnapshot(ctx context.Context, tabID string) (Screen, error) {
	return s.Snapshot(ctx, tabID)
}

type createSessionTerminal struct {
	fakeTerminal
	sessionsMu sync.Mutex
	sessionIDs []string
}

func (c *createSessionTerminal) Create(ctx context.Context, sessionID string, options CreateTerminalOptions) (CreatedTab, error) {
	c.sessionsMu.Lock()
	c.sessionIDs = append(c.sessionIDs, sessionID)
	c.sessionsMu.Unlock()
	return c.fakeTerminal.Create(ctx, sessionID, options)
}

func TestWaitForTargetsCreatedTabAndRequiresQuiet(t *testing.T) {
	terminal := &scriptedSnapshotTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal, TabSession: func(string) string { return "s" }, PollInterval: time.Millisecond})
	scope := Scope{SessionID: "s", TabID: "scope-tab"}
	created := registry.Execute(context.Background(), "job", scope, Call{ID: "create", Name: "terminal_create"}, nil)
	if !created.OK {
		t.Fatalf("create = %+v", created)
	}
	terminal.screens = []Screen{
		{Text: "READY", Tail: []string{"READY"}, IdleMS: 5},
		{Text: "READY", Tail: []string{"READY"}, IdleMS: 80},
	}
	result := registry.Execute(context.Background(), "job", scope, Call{ID: "wait", Name: "wait_for", Args: json.RawMessage(`{"tab_id":"tab-new","pattern":"READY","quiet_ms":50,"timeout_ms":1000}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "安静 80ms") {
		t.Fatalf("wait_for = %+v", result)
	}
	foreignJob := registry.Execute(context.Background(), "other-job", scope, Call{ID: "wait-other", Name: "wait_for", Args: json.RawMessage(`{"tab_id":"tab-new","pattern":"READY"}`)}, nil)
	if foreignJob.OK || !strings.Contains(foreignJob.Text, ErrCrossScope.Error()) {
		t.Fatalf("foreign job wait = %+v", foreignJob)
	}
}

func TestWaitForQuietOnly(t *testing.T) {
	terminal := &scriptedSnapshotTerminal{screens: []Screen{{IdleMS: 10}, {IdleMS: 60}}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "wait", Name: "wait_for", Args: json.RawMessage(`{"quiet_ms":50,"timeout_ms":1000}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "安静 60ms") {
		t.Fatalf("quiet-only wait = %+v", result)
	}
	invalid := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "invalid", Name: "wait_for", Args: json.RawMessage(`{}`)}, nil)
	if invalid.OK || !strings.Contains(invalid.Text, "pattern") {
		t.Fatalf("missing pattern and quiet_ms = %+v", invalid)
	}
	emptyPattern := NewRegistry(Dependencies{Terminal: &fakeTerminal{}}).Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "empty", Name: "wait_for", Args: json.RawMessage(`{"pattern":""}`)}, nil)
	if !emptyPattern.OK {
		t.Fatalf("explicit empty pattern must preserve immediate match: %+v", emptyPattern)
	}
}

func TestWaitForNewOutputAlsoRequiresQuiet(t *testing.T) {
	ring := newFakeRing(0, "xREADY")
	terminal := &scriptedSnapshotTerminal{screens: []Screen{
		{Cols: 80, Rows: 24, Seq: ring.latest},
		{Cols: 80, Rows: 24, Seq: ring.latest, IdleMS: 5},
		{Cols: 80, Rows: 24, Seq: ring.latest, IdleMS: 70},
	}}
	terminal.outputSinceFunc = ring.since
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "wait", Name: "wait_for", Args: json.RawMessage(`{"pattern":"READY","since_seq":1,"quiet_ms":50,"timeout_ms":1000}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "安静 70ms") || !strings.Contains(result.Text, "新输出") {
		t.Fatalf("anchored quiet wait = %+v", result)
	}
}

func TestSendKeysPasteEncodingAndSubmissionProof(t *testing.T) {
	t.Run("bracketed", func(t *testing.T) {
		terminal := &fakeTerminal{screen: Screen{Seq: 42, BracketedPaste: true}}
		registry := NewRegistry(Dependencies{Terminal: terminal})
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"one\n<enter>two","enter":true,"paste":true}`)}, nil)
		if !result.OK {
			t.Fatalf("send_keys = %+v", result)
		}
		if len(terminal.writes) != 2 || string(terminal.writes[0]) != "\x1b[200~one\n<enter>two\x1b[201~" || string(terminal.writes[1]) != "\r" {
			t.Fatalf("writes = %q", terminal.writes)
		}
		if result.SubmittedSeq == nil || *result.SubmittedSeq != 42 || !result.WriteConfirmed || !strings.Contains(result.Notes, "not proof") {
			t.Fatalf("submission proof = %+v", result)
		}
	})
	t.Run("plain paste", func(t *testing.T) {
		// Unbracketed mode maps LF to CR, so a multi-line paste would submit
		// early: the mode-aware guard rejects it before any write.
		terminal := &fakeTerminal{screen: Screen{Seq: 42, BracketedPaste: false}}
		registry := NewRegistry(Dependencies{Terminal: terminal})
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"one\n<enter>two","enter":true,"paste":true}`)}, nil)
		if result.OK || !strings.Contains(result.Text, "bracketed-paste") {
			t.Fatalf("plain paste = %+v", result)
		}
		if len(terminal.writes) != 0 {
			t.Fatalf("plain paste wrote bytes: %q", terminal.writes)
		}
	})
}

func TestSendKeysWithoutPasteKeepsKeyEncoding(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{BracketedPaste: true}}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"a<enter>","enter":true}`)}, nil)
	if !result.OK || len(terminal.writes) != 1 || string(terminal.writes[0]) != "a\r\r" || result.SubmittedSeq != nil {
		t.Fatalf("ordinary send_keys = %+v writes=%q", result, terminal.writes)
	}
}

func TestTerminalCreateByAssetBindsCreatedSession(t *testing.T) {
	terminal := &createSessionTerminal{fakeTerminal: fakeTerminal{screen: Screen{Text: "READY", Tail: []string{"READY"}}}}
	var resolveCalls []string
	registry := NewRegistry(Dependencies{
		Terminal: terminal,
		ResolveAssetSession: func(_ context.Context, assetID string) (string, error) {
			resolveCalls = append(resolveCalls, assetID)
			if assetID != "asset-b" {
				return "", errors.New("asset does not belong to owner")
			}
			return "session-b", nil
		},
		TabSession: func(tabID string) string {
			if tabID == "tab-new" {
				return "session-b"
			}
			return "session-a"
		},
	})
	scope := Scope{SessionID: "session-a", TabID: "tab-a"}
	created := registry.Execute(context.Background(), "job", scope, Call{ID: "create", Name: "terminal_create", Args: json.RawMessage(`{"asset_id":"asset-b"}`)}, nil)
	if !created.OK || created.Terminal == nil || created.Terminal.SessionID != "session-b" || len(resolveCalls) != 1 {
		t.Fatalf("create = %+v resolver calls=%v", created, resolveCalls)
	}
	if len(terminal.sessionIDs) != 1 || terminal.sessionIDs[0] != "session-b" {
		t.Fatalf("created sessions = %v", terminal.sessionIDs)
	}
	wait := registry.Execute(context.Background(), "job", scope, Call{ID: "wait", Name: "wait_for", Args: json.RawMessage(`{"tab_id":"tab-new","pattern":"READY"}`)}, nil)
	if !wait.OK {
		t.Fatalf("wait on asset-created tab = %+v", wait)
	}
	rejected := registry.Execute(context.Background(), "job", scope, Call{ID: "create-foreign", Name: "terminal_create", Args: json.RawMessage(`{"asset_id":"foreign"}`)}, nil)
	if rejected.OK || len(terminal.sessionIDs) != 1 {
		t.Fatalf("foreign create = %+v sessions=%v", rejected, terminal.sessionIDs)
	}
}
