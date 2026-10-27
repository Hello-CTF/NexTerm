package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/durable"
)

// A custom title made of emoji sequences is accepted up to the rune limit,
// stored by the helper unchanged and recovered unchanged after a restart.
func TestDurableEmojiTitleSurvivesRestart(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "emoji-tab", "a-1", false)
	// 11 seven-rune family emoji plus "构建机" is exactly maxTabTitleRunes.
	title := strings.Repeat("👨\u200d👩\u200d👧\u200d👦", 11) + "构建机"
	if n := utf8.RuneCountInString(title); n != maxTabTitleRunes {
		t.Fatalf("fixture has %d runes", n)
	}
	renamed, err := first.RenameTabAt(context.Background(), info.ID, title, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != title {
		t.Fatalf("renamed title = %q, want %q", renamed.Title, title)
	}
	if _, err := first.RenameTab(context.Background(), info.ID, title+"🙂"); !errors.Is(err, ErrInvalidTitle) {
		t.Fatalf("over-limit rename err = %v, want ErrInvalidTitle", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, _, _, secondSession := newDurableTestManager(t, provider)
	recovered := recoverCanonicalDurableTestTab(t, second, secondSession, info.ID, "b-1")
	if recovered.Title != title || !recovered.TitleCustom {
		t.Fatalf("recovered = %+v, want title %q", recovered, title)
	}
}

// A canonical helper title carries no decision time, so recovery stamps it.
func TestDurableCanonicalRecoveryStampsDecisionTime(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "canonical-timed"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.closeTab(first, false); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, Cols: 80, Rows: 24, Detached: true,
		Durable: &DurableTabOptions{Recover: true, Title: "deploy", Identity: recoveryIdentity(manager, tabID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !info.TitleCustom || info.TitleChangedAt < before {
		t.Fatalf("recovered = %+v, want a decision time stamped at recovery (>= %d)", info, before)
	}
}

// A title another instance set through the shared helper is adopted with a
// fresh decision time, so it does not look older than a stale client's.
func TestHelperTitleAdoptionStampsDecisionTime(t *testing.T) {
	provider := &unlatchMetadataProvider{fakeDurableProvider: newFakeDurableProvider(), incarnation: "inc-1"}
	manager, _, _, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "helper-adopt"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "adopt-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTabAt(ctx, tabID, "deploy", 1_000); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	// Another instance renamed the tab on the helper; this rename attempt
	// refreshes the descriptor and adopts it.
	provider.setDescriptor("ops", 2, durable.ErrIdentity)
	if _, err := manager.RenameTabAt(ctx, tabID, "other", 1_500); err == nil {
		t.Fatal("RenameTabAt = nil, want the identity error surfaced")
	}
	info := tab.Info()
	if info.Title != "ops" || !info.TitleCustom || info.TitleChangedAt < before {
		t.Fatalf("adopted = %+v, want the helper title stamped now (>= %d)", info, before)
	}
}

// A client decision time far ahead of the backend clock is clamped, so a
// skewed client cannot pin its title against every later decision.
func TestRenameTabClampsFutureDecisionTime(t *testing.T) {
	manager, _, _, connected := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	info, err := manager.RenameTabAt(context.Background(), tab.ID, "deploy", far)
	if err != nil {
		t.Fatal(err)
	}
	limit := time.Now().Add(maxTitleClockSkew).UnixMilli()
	if info.TitleChangedAt >= far || info.TitleChangedAt > limit {
		t.Fatalf("decision time = %d, want clamped to <= %d", info.TitleChangedAt, limit)
	}
}
