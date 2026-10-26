package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
)

// timedTitleProvider is a durable provider whose title store also keeps each
// title's decision time, like the production store.
type timedTitleProvider struct {
	*fakeDurableProvider
	times map[string]int64
}

func newTimedTitleProvider() *timedTitleProvider {
	return &timedTitleProvider{fakeDurableProvider: newFakeDurableProvider(), times: map[string]int64{}}
}

func (p *timedTitleProvider) DurableTabTitleChangedAtContext(_ context.Context, tabID string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.times[tabID]
}

func (p *timedTitleProvider) PersistDurableTabTitleAtContext(ctx context.Context, tabID, title string, changedAt int64) {
	p.PersistDurableTabTitleContext(ctx, tabID, title)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.times[tabID] = changedAt
}

// A restart reports the persisted decision time, so a stale timestamped
// client decision cannot overwrite the newer title.
func TestDurableTitleDecisionTimeSurvivesRestart(t *testing.T) {
	provider := newTimedTitleProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "timed-tab", "a-1", false)
	at := time.Now().UnixMilli() - 60_000
	if _, err := first.RenameTabAt(context.Background(), info.ID, "构建机", at); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, _, _, secondSession := newDurableTestManager(t, provider)
	recovered := openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	if recovered.Title != "构建机" || !recovered.TitleCustom || recovered.TitleChangedAt != at {
		t.Fatalf("recovered = %+v, want the persisted decision time %d", recovered, at)
	}
}

// A title persisted without a decision time is stamped when the backend
// first observes it on recovery, never reported as unknown (zero).
func TestDurableTitleWithoutDecisionTimeIsStampedOnRecovery(t *testing.T) {
	provider := newFakeDurableProvider()
	first, _, _, firstSession := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, first, firstSession, "legacy-tab", "a-1", false)
	if _, err := first.RenameTab(context.Background(), info.ID, "构建机"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	second, _, _, secondSession := newDurableTestManager(t, provider)
	recovered := openDurableTestTab(t, second, secondSession, info.ID, "b-1", true)
	if !recovered.TitleCustom || recovered.TitleChangedAt < before {
		t.Fatalf("recovered = %+v, want a decision time stamped at recovery (>= %d)", recovered, before)
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
		Durable: &DurableTabOptions{Recover: true, CanonicalMetadata: true, Title: "deploy", Identity: recoveryIdentity(manager, tabID)},
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
