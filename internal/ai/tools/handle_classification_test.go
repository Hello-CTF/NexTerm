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
