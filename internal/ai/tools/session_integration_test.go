package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// The session adapter consumes the merged terminal primitives: TabDetail
// follows the session manager's custom-title precedence (a user/AI-set title
// wins over the OSC 0/2 dynamic title, which surfaces only while no custom
// title is set), and WritePasteLiteral delegates to the session manager's
// write-lock guarded literal paste, so send_keys no longer marks the
// on→off mode-flip window as uncovered for session-backed terminals.

func TestSessionTabDetailCustomTitlePrecedence(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	ctx := trustedContext()

	// Without a shell-reported title the durable user/AI-set title shows.
	if _, err := fixture.manager.RenameTab(ctx, fixture.tabID, "构建机"); err != nil {
		t.Fatal(err)
	}
	detail, err := adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "构建机" {
		t.Fatalf("durable title detail = %+v err=%v", detail, err)
	}
	// Once the application reports an OSC 2 title, the custom title keeps
	// precedence: the live OSC report must not replace a user/AI-set title.
	fixture.terminal().Feed([]byte("\x1b]2;vim build\x07"))
	detail, err = adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "构建机" {
		t.Fatalf("OSC 2 title detail = %+v err=%v", detail, err)
	}
	// OSC 0 sets the window title too, and still loses to the custom title.
	fixture.terminal().Feed([]byte("\x1b]0;icon title\x07"))
	detail, err = adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "构建机" {
		t.Fatalf("OSC 0 title detail = %+v err=%v", detail, err)
	}
	// The read_screen header carries the same custom title.
	result := fixture.registry().Execute(ctx, "job", fixture.scope(), Call{ID: "r", Name: "read_screen"}, nil)
	if !result.OK || !strings.Contains(result.Text, "「构建机」") {
		t.Fatalf("read_screen header = %+v", result)
	}
}

func TestSessionTabDetailOSCTitleWithoutCustomTitle(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	ctx := trustedContext()

	// No custom title is set: the OSC 2 dynamic title flows through.
	fixture.terminal().Feed([]byte("\x1b]2;vim build\x07"))
	detail, err := adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "vim build" {
		t.Fatalf("OSC 2 title detail = %+v err=%v", detail, err)
	}
	// OSC 0 updates the dynamic title the same way.
	fixture.terminal().Feed([]byte("\x1b]0;icon title\x07"))
	detail, err = adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "icon title" {
		t.Fatalf("OSC 0 title detail = %+v err=%v", detail, err)
	}
	// The read_screen header carries the live dynamic title.
	result := fixture.registry().Execute(ctx, "job", fixture.scope(), Call{ID: "r", Name: "read_screen"}, nil)
	if !result.OK || !strings.Contains(result.Text, "「icon title」") {
		t.Fatalf("read_screen header = %+v", result)
	}
	// A later custom title takes precedence over the dynamic title.
	if _, err := fixture.manager.RenameTab(ctx, fixture.tabID, "构建机"); err != nil {
		t.Fatal(err)
	}
	detail, err = adapter.(TabDetailTerminal).TabDetail(ctx, fixture.tabID)
	if err != nil || detail.Title != "构建机" {
		t.Fatalf("custom title detail = %+v err=%v", detail, err)
	}
}

func TestSessionAdapterWritePasteLiteralGuard(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	ctx := trustedContext()
	literal, ok := adapter.(PasteLiteralTerminal)
	if !ok {
		t.Fatal("session adapter does not implement PasteLiteralTerminal")
	}

	// Bracketed paste off: multi-line text is refused before any byte.
	if err := literal.WritePasteLiteral(ctx, fixture.tabID, "line1\nline2"); !errors.Is(err, session.ErrPasteGuarded) {
		t.Fatalf("unguarded multi-line paste err = %v", err)
	}
	if writes := fixture.channel.successfulWrites(); len(writes) != 0 {
		t.Fatalf("refused paste wrote %q", writes)
	}
	// Single-line text is written unchanged regardless of mode.
	if err := literal.WritePasteLiteral(ctx, fixture.tabID, "ls -l"); err != nil {
		t.Fatalf("single-line literal paste err = %v", err)
	}
	// Bracketed paste on: multi-line text is written bracketed under the lock.
	if err := fixture.channel.emit([]byte("\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}
	fixture.waitForMode(t, true)
	if err := literal.WritePasteLiteral(ctx, fixture.tabID, "line1\nline2"); err != nil {
		t.Fatalf("bracketed literal paste err = %v", err)
	}
	writes := fixture.channel.successfulWrites()
	if len(writes) != 2 || string(writes[0]) != "ls -l" || string(writes[1]) != "\x1b[200~line1\nline2\x1b[201~" {
		t.Fatalf("literal paste writes = %q", writes)
	}
}

func TestSendKeysSessionPasteUsesLiteralGuard(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	if err := fixture.channel.emit([]byte("\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}
	fixture.waitForMode(t, true)
	result := fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "p", Name: "send_keys",
		Args: json.RawMessage(`{"keys":"line1\nline2","paste":true,"enter":true,"submit_delay_ms":1}`)}, nil)
	if !result.OK {
		t.Fatalf("paste send_keys = %+v", result)
	}
	// The write-lock guarded literal paste is now wired, so the on→off
	// mode-flip window note is gone; the paste-submit capability has not
	// merged, so the split-write note stays.
	if strings.Contains(result.Text, "on→off 切换窗口未覆盖") || strings.Contains(result.Notes, "on→off") {
		t.Fatalf("stale mode-flip degradation note: %+v", result)
	}
	if !strings.Contains(result.Text, "分次写入") {
		t.Fatalf("split-write note missing: %+v", result)
	}
	writes := fixture.channel.successfulWrites()
	if len(writes) != 2 || string(writes[0]) != "\x1b[200~line1\nline2\x1b[201~" || string(writes[1]) != "\r" {
		t.Fatalf("paste writes = %q", writes)
	}
}

func TestSendKeysSessionPasteNewlineRefusedWhenModeOff(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	result := fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "p", Name: "send_keys",
		Args: json.RawMessage(`{"keys":"line1\nline2","paste":true}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "bracketed-paste") {
		t.Fatalf("unguarded newline paste = %+v", result)
	}
	if writes := fixture.channel.successfulWrites(); len(writes) != 0 {
		t.Fatalf("refused paste wrote %q", writes)
	}
}
