package tools

import (
	"context"
	"strings"
	"testing"
)

func TestReferencedTabIsReadableAcrossSessions(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t9", "s9", "other$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}

	before := registry.Execute(context.Background(), "job", scope, p1Call("r0", "read_screen", `{"tab_id":"t9"}`), nil)
	if before.OK || !strings.Contains(before.Text, ErrCrossScope.Error()) {
		t.Fatalf("unreferenced read = %+v", before)
	}

	granted := registry.GrantReferencedTabs(context.Background(), "job", scope, []string{"t9", "missing"})
	if len(granted) != 1 || granted[0].Handle.String() != "t/t9/s9" || !granted[0].ReadOnly {
		t.Fatalf("granted = %v", granted)
	}
	for _, target := range []string{"t9", "t/t9/s9"} {
		result := registry.Execute(context.Background(), "job", scope, p1Call("r-"+target, "read_screen", `{"tab_id":"`+target+`"}`), nil)
		if !result.OK || !strings.Contains(result.Text, "other$") {
			t.Fatalf("referenced read %s = %+v", target, result)
		}
	}
	wait := registry.Execute(context.Background(), "job", scope, p1Call("w", "wait_for", `{"tab_id":"t/t9/s9","pattern":"other"}`), nil)
	if !wait.OK {
		t.Fatalf("referenced wait_for = %+v", wait)
	}
	if target, ok := registry.ResolveCallTarget(context.Background(), "job", scope, p1Call("r", "read_screen", `{"tab_id":"t9"}`)); !ok || target.String() != "t/t9/s9" {
		t.Fatalf("read target = %v ok=%v", target, ok)
	}

	keys := registry.Execute(context.Background(), "job", scope, p1Call("k", "send_keys", `{"tab_id":"t/t9/s9","keys":"ls"}`), nil)
	if keys.OK || !strings.Contains(keys.Text, ErrCrossScope.Error()) {
		t.Fatalf("send_keys on referenced tab = %+v", keys)
	}
	rename := registry.Execute(context.Background(), "job", scope, p1Call("n", "terminal_rename", `{"tab_id":"t9","title":"x"}`), nil)
	if rename.OK || !strings.Contains(rename.Text, ErrCrossScope.Error()) {
		t.Fatalf("rename on referenced tab = %+v", rename)
	}
	if _, ok := registry.ResolveCallTarget(context.Background(), "job", scope, p1Call("k", "send_keys", `{"tab_id":"t9"}`)); ok {
		t.Fatal("send_keys must not resolve a referenced tab")
	}

	other := registry.Execute(context.Background(), "job2", scope, p1Call("r2", "read_screen", `{"tab_id":"t9"}`), nil)
	if other.OK || !strings.Contains(other.Text, ErrCrossScope.Error()) {
		t.Fatalf("grant leaked to another job = %+v", other)
	}
}

func TestReferencedTabDetectsReplacement(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t9", "s9", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	registry.GrantReferencedTabs(context.Background(), "job", scope, []string{"t9"})

	fake.mu.Lock()
	fake.tabs["t9"] = fakeTab("t9", "s10", "$ ", 0)
	fake.mu.Unlock()
	result := registry.Execute(context.Background(), "job", scope, p1Call("r", "read_screen", `{"tab_id":"t9"}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalReplaced.Error()) {
		t.Fatalf("replaced referenced tab = %+v", result)
	}

	fake.mu.Lock()
	delete(fake.tabs, "t9")
	fake.mu.Unlock()
	result = registry.Execute(context.Background(), "job", scope, p1Call("r2", "read_screen", `{"tab_id":"t9"}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("closed referenced tab = %+v", result)
	}
}

func TestTerminalListMarksAccess(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t8", "s8", "$ ", 0), fakeTab("t9", "s9", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	registry.GrantReferencedTabs(context.Background(), "job", scope, []string{"t9"})

	result := registry.Execute(context.Background(), "job", scope, p1Call("l", "terminal_list", `{}`), nil)
	if !result.OK {
		t.Fatalf("terminal_list = %+v", result)
	}
	lines := map[string]string{}
	for _, line := range strings.Split(result.Text, "\n") {
		if handle, _, ok := strings.Cut(line, " "); ok && strings.HasPrefix(handle, "t/") {
			lines[handle] = line
		}
	}
	if line := lines["t/t1/s1"]; strings.Contains(line, "read_only") || strings.Contains(line, "outside_scope") {
		t.Fatalf("current tab marked: %q", line)
	}
	if !strings.HasSuffix(lines["t/t9/s9"], " read_only") {
		t.Fatalf("referenced tab line: %q", lines["t/t9/s9"])
	}
	if !strings.HasSuffix(lines["t/t8/s8"], " outside_scope") {
		t.Fatalf("unreferenced tab line: %q", lines["t/t8/s8"])
	}
}

func TestGrantReferencedTabsMarksOwnConnectionWritable(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t2", "s1", "$ ", 0), fakeTab("t9", "s9", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	granted := registry.GrantReferencedTabs(context.Background(), "job", scope, []string{"t1", "t2", "t9"})
	access := map[string]bool{}
	for _, terminal := range granted {
		access[terminal.Handle.TabID] = terminal.ReadOnly
	}
	if len(access) != 3 || access["t1"] || access["t2"] || !access["t9"] {
		t.Fatalf("granted = %+v", granted)
	}
	keys := registry.Execute(context.Background(), "job", scope, p1Call("k", "send_keys", `{"tab_id":"t/t2/s1","keys":"ls"}`), nil)
	if !keys.OK {
		t.Fatalf("send_keys on a referenced tab of the own connection = %+v", keys)
	}
}

func TestGrantReferencedTabsNeedsTabScope(t *testing.T) {
	fake := newTabFake(fakeTab("t9", "s9", "$ ", 0))
	registry := p1Registry(fake)
	if granted := registry.GrantReferencedTabs(context.Background(), "job", Scope{SessionID: "s1"}, []string{"t9"}); len(granted) != 0 {
		t.Fatalf("granted without a tab scope = %+v", granted)
	}
}

func TestReferencedTabInMultiTabWait(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "READY", 0), fakeTab("t9", "s9", "READY", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	registry.GrantReferencedTabs(context.Background(), "job", scope, []string{"t9"})
	// Multi-tab waits only match output after they start, so this one times
	// out; what matters is that both targets resolve.
	result := registry.Execute(context.Background(), "job", scope, p1Call("w", "wait_for", `{"pattern":"READY","tab_ids":["t/t1/s1","t/t9/s9"],"timeout_ms":20}`), nil)
	if strings.Contains(result.Text, ErrCrossScope.Error()) || result.Wait == nil {
		t.Fatalf("multi-tab wait with a referenced tab = %+v", result)
	}
}
