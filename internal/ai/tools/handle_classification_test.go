package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// Gone is a verified-absence classification: only an explicit not-found may
// map to it. Transient, forbidden, and internal errors must propagate as
// themselves so callers never rebuild or retry on a false "gone" verdict.

type tabSessionErrorTerminal struct {
	*tabFakeTerminal
	err error
}

func (t *tabSessionErrorTerminal) TabSession(context.Context, string) (string, error) {
	return "", t.err
}

func TestTerminalHandleErrorClassification(t *testing.T) {
	transient := errors.New("backend temporarily unreachable")
	for name, test := range map[string]struct {
		err      error
		want     string
		wantGone bool
	}{
		"not found maps to gone": {err: session.ErrTabNotFound, want: ErrTerminalGone.Error(), wantGone: true},
		"forbidden propagates":   {err: session.ErrForbidden, want: session.ErrForbidden.Error()},
		"transient propagates":   {err: transient, want: transient.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &tabSessionErrorTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0)), err: test.err}
			registry := p1Registry(fake)
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
				p1Call("r", "read_screen", `{"tab_id":"t/t1/s1"}`), nil)
			if result.OK {
				t.Fatalf("handle resolved despite %v", test.err)
			}
			if !strings.Contains(result.Text, test.want) {
				t.Fatalf("error %v misclassified as %q", test.err, result.Text)
			}
			if test.wantGone && !strings.Contains(result.Text, ErrTerminalGone.Error()) {
				t.Fatalf("not-found did not map to gone: %q", result.Text)
			}
			if !test.wantGone && strings.Contains(result.Text, ErrTerminalGone.Error()) {
				t.Fatalf("error %v masqueraded as gone: %q", test.err, result.Text)
			}
		})
	}
}

// ResolveCallTarget resolves only single-tab terminal tools, with the same
// scope and handle rules the execution applies; every other shape — a
// multi-tab wait, a non-terminal tool, a cross-scope or unresolvable
// target — reports no target instead of guessing one.
func TestResolveCallTargetSingleTabTools(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t2", "s2", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	ctx := context.Background()

	for _, call := range []Call{
		p1Call("a", "send_keys", `{"keys":"ls"}`),
		p1Call("b", "read_screen", `{}`),
		p1Call("c", "terminal_rename", `{"tab_id":"t1","title":"x"}`),
		p1Call("d", "wait_for", `{"pattern":"READY"}`),
		p1Call("e", "wait_for", `{"pattern":"READY","tab_ids":["t/t1/s1"]}`),
		p1Call("f", "read_screen", `{"tab_id":"t/t1/s1"}`),
	} {
		handle, ok := registry.ResolveCallTarget(ctx, "job", scope, call)
		if !ok {
			t.Fatalf("%s did not resolve: %+v", call.Name, call.Args)
		}
		if handle.TabID != "t1" || handle.SessionID != "s1" {
			t.Fatalf("%s resolved %+v, want t1/s1", call.Name, handle)
		}
		if parsed, err := ParseTerminalHandle(handle.String()); err != nil || parsed != handle {
			t.Fatalf("%s handle %q not parseable: %v", call.Name, handle.String(), err)
		}
	}
}

func TestResolveCallTargetUnknownShapes(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0), fakeTab("t2", "s2", "$ ", 0))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	ctx := context.Background()

	unknown := []Call{
		// Multi-tab waits have no single honest target.
		p1Call("a", "wait_for", `{"pattern":"READY","tab_ids":["t/t1/s1","t/t2/s2"]}`),
		p1Call("b", "wait_for", `{"pattern":"READY","tab_id":"t1","tab_ids":["t/t1/s1"]}`),
		// Non-terminal tools never carry a tab target.
		p1Call("c", "exec_commands", `{"commands":["ls"]}`),
		p1Call("d", "db_query", `{"query":"select 1"}`),
		p1Call("e", "terminal_create", `{}`),
		p1Call("f", "terminal_list", `{}`),
		// Cross-scope, gone, and malformed targets are unverifiable.
		p1Call("g", "send_keys", `{"keys":"ls","tab_id":"t2"}`),
		p1Call("h", "read_screen", `{"tab_id":"t/t1/s9"}`),
		p1Call("i", "read_screen", `{"tab_id":"t/t9/s1"}`),
		p1Call("j", "send_keys", `{"keys":"ls","tab_id":"bogus"}`),
	}
	for _, call := range unknown {
		if handle, ok := registry.ResolveCallTarget(ctx, "job", scope, call); ok {
			t.Fatalf("%s %s resolved %+v, want unknown", call.Name, call.Args, handle)
		}
	}
	// A scope without a tab cannot anchor a default target either.
	if handle, ok := registry.ResolveCallTarget(ctx, "job", Scope{}, p1Call("k", "read_screen", `{}`)); ok {
		t.Fatalf("scope-less read_screen resolved %+v", handle)
	}
}
