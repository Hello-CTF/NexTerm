package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type fakeRunProbe struct {
	mu       sync.Mutex
	statuses map[string]RosterRuntimeStatus
	errors   map[string]error
}

func newFakeRunProbe() *fakeRunProbe {
	return &fakeRunProbe{statuses: map[string]RosterRuntimeStatus{}, errors: map[string]error{}}
}

func (p *fakeRunProbe) set(runID string, kind RosterRuntimeKind, currentIncarnation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses[runID] = RosterRuntimeStatus{Kind: kind, CurrentIncarnation: currentIncarnation}
}

func (p *fakeRunProbe) setError(runID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errors[runID] = err
}

func (p *fakeRunProbe) ProbeRunRuntime(_ context.Context, runID string) (RosterRuntimeStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.errors[runID]; err != nil {
		return RosterRuntimeStatus{}, err
	}
	if status, ok := p.statuses[runID]; ok {
		return status, nil
	}
	return RosterRuntimeStatus{Kind: RosterRuntimeUnverifiable}, nil
}

func recoveryFixture(t *testing.T) (*store.Store, context.Context, string) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := db.ConvCreate(ctx, "recovery", nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, ctx, conversation.ID
}

func insertRun(t *testing.T, db *store.Store, ctx context.Context, id, conversationID, status string, finished bool) {
	t.Helper()
	run := store.RunRow{
		ID: id, ConversationID: conversationID, Status: status,
		CreatedAt: int64(1000 + len(id)), UpdatedAt: int64(1000 + len(id)),
	}
	if finished {
		now := int64(2000)
		run.FinishedAt = &now
	}
	if err := db.RunInsert(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestRunRecoveryClassifiesAndWritesOnlyAuthoritative(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	cases := []struct {
		runID  string
		kind   RosterRuntimeKind
		status string
	}{
		{"run-connected", RosterRuntimeConnected, store.RunStatusRunning},
		{"run-disconnected", RosterRuntimeDisconnected, store.RunStatusInterrupted},
		{"run-gone", RosterRuntimeGone, store.RunStatusRunning},
		{"run-exited", RosterRuntimeExited, store.RunStatusInterrupted},
		{"run-replaced", RosterRuntimeReplaced, store.RunStatusRunning},
		{"run-unverifiable", RosterRuntimeUnverifiable, store.RunStatusRunning},
	}
	probe := newFakeRunProbe()
	for _, caseInfo := range cases {
		insertRun(t, db, ctx, caseInfo.runID, conversationID, caseInfo.status, false)
		probe.set(caseInfo.runID, caseInfo.kind, "inc-new")
	}

	summary, err := NewRunRecovery(db).Recover(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != len(cases) {
		t.Fatalf("examined %d runs, want %d", len(summary.Entries), len(cases))
	}
	counts := map[RunRecoveryState]int{}
	written := map[string]bool{}
	for _, entry := range summary.Entries {
		counts[entry.State]++
		if entry.Reason == "" {
			t.Fatalf("entry %s has no auditable reason", entry.RunID)
		}
		if entry.Wrote {
			written[entry.RunID] = true
		}
	}
	for _, state := range []RunRecoveryState{RunRecoveryConnected, RunRecoveryDisconnected, RunRecoveryGone, RunRecoveryExited, RunRecoveryReplaced, RunRecoveryUnverifiable} {
		if counts[state] != 1 {
			t.Fatalf("state %s = %d, want 1", state, counts[state])
		}
	}
	// Only the authoritative outcomes wrote.
	for _, runID := range []string{"run-connected", "run-disconnected", "run-unverifiable"} {
		if written[runID] {
			t.Fatalf("%s was written without authority", runID)
		}
	}
	for _, runID := range []string{"run-gone", "run-exited", "run-replaced"} {
		if !written[runID] {
			t.Fatalf("%s was not written despite authority", runID)
		}
	}
	// The writes landed as the terminal states.
	gone, _ := db.RunGet(ctx, "run-gone")
	if gone.Status != store.RunStatusFailed || gone.FinishedAt == nil || gone.Error != "runtime gone" {
		t.Fatalf("gone run = %+v", gone)
	}
	exited, _ := db.RunGet(ctx, "run-exited")
	if exited.Status != store.RunStatusInterrupted || exited.FinishedAt == nil {
		t.Fatalf("exited run = %+v", exited)
	}
	replaced, _ := db.RunGet(ctx, "run-replaced")
	if replaced.Status != RunStatusSuperseded || replaced.FinishedAt == nil {
		t.Fatalf("replaced run = %+v", replaced)
	}
	// The unwritten runs stay active and unfinished.
	connected, _ := db.RunGet(ctx, "run-connected")
	if connected.Status != store.RunStatusRunning || connected.FinishedAt != nil {
		t.Fatalf("connected run = %+v", connected)
	}
	unverifiable, _ := db.RunGet(ctx, "run-unverifiable")
	if unverifiable.Status != store.RunStatusRunning || unverifiable.FinishedAt != nil {
		t.Fatalf("unverifiable run = %+v", unverifiable)
	}
}

func TestRunRecoveryCanceledAndFinishedUntouched(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	insertRun(t, db, ctx, "run-canceled", conversationID, store.RunStatusCanceled, true)
	insertRun(t, db, ctx, "run-completed", conversationID, store.RunStatusCompleted, true)
	insertRun(t, db, ctx, "run-failed", conversationID, store.RunStatusFailed, true)

	summary, err := NewRunRecovery(db).Recover(ctx, newFakeRunProbe())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != 0 {
		t.Fatalf("terminal runs were re-examined: %+v", summary.Entries)
	}
	for _, runID := range []string{"run-canceled", "run-completed", "run-failed"} {
		run, err := db.RunGet(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.UpdatedAt != int64(1000+len(runID)) {
			t.Fatalf("%s was touched: %+v", runID, run)
		}
	}
}

func TestRunRecoveryOwnerIsolation(t *testing.T) {
	db, ctxA, conversationID := recoveryFixture(t)
	insertRun(t, db, ctxA, "run-a", conversationID, store.RunStatusRunning, false)
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	conversationB, err := db.ConvCreate(ctxB, "recovery-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	insertRun(t, db, ctxB, "run-b", conversationB.ID, store.RunStatusRunning, false)
	probe := newFakeRunProbe()
	probe.set("run-a", RosterRuntimeGone, "")
	probe.set("run-b", RosterRuntimeGone, "")

	summaryA, err := NewRunRecovery(db).Recover(ctxA, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaryA.Entries) != 1 || summaryA.Entries[0].RunID != "run-a" {
		t.Fatalf("owner A summary = %+v", summaryA.Entries)
	}
	runB, _ := db.RunGet(ctxB, "run-b")
	if runB.Status != store.RunStatusRunning || runB.FinishedAt != nil {
		t.Fatalf("cross-owner run was written: %+v", runB)
	}
	summaryB, err := NewRunRecovery(db).Recover(ctxB, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaryB.Entries) != 1 || summaryB.Entries[0].RunID != "run-b" || summaryB.Gone != 1 {
		t.Fatalf("owner B summary = %+v", summaryB.Entries)
	}
}

func TestRunRecoveryLargeSet(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	const total = 120
	kinds := []RosterRuntimeKind{RosterRuntimeConnected, RosterRuntimeDisconnected, RosterRuntimeGone, RosterRuntimeExited, RosterRuntimeReplaced, RosterRuntimeUnverifiable}
	probe := newFakeRunProbe()
	for i := 0; i < total; i++ {
		runID := fmt.Sprintf("run-%03d", i)
		status := store.RunStatusRunning
		if i%2 == 1 {
			status = store.RunStatusInterrupted
		}
		insertRun(t, db, ctx, runID, conversationID, status, false)
		probe.set(runID, kinds[i%len(kinds)], "inc-new")
	}

	summary, err := NewRunRecovery(db).Recover(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != total {
		t.Fatalf("examined %d runs, want %d", len(summary.Entries), total)
	}
	seen := map[string]bool{}
	for _, entry := range summary.Entries {
		if seen[entry.RunID] {
			t.Fatalf("run %s examined twice", entry.RunID)
		}
		seen[entry.RunID] = true
	}
	// Each kind appears exactly total/6 times; only the three authoritative
	// kinds write.
	if summary.Connected != 20 || summary.Disconnected != 20 || summary.Gone != 20 || summary.Exited != 20 || summary.Replaced != 20 || summary.Unverifiable != 20 {
		t.Fatalf("counts = %+v", summary)
	}
	written := 0
	for _, entry := range summary.Entries {
		if entry.Wrote {
			written++
			if entry.State != RunRecoveryGone && entry.State != RunRecoveryExited && entry.State != RunRecoveryReplaced {
				t.Fatalf("non-authoritative write: %+v", entry)
			}
		}
	}
	if written != 60 {
		t.Fatalf("written = %d, want 60", written)
	}
}

func TestRunRecoveryProbeErrorIsUnverifiable(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	insertRun(t, db, ctx, "run-err", conversationID, store.RunStatusRunning, false)
	probe := newFakeRunProbe()
	probe.setError("run-err", errors.New("probe transport failed"))

	summary, err := NewRunRecovery(db).Recover(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != 1 || summary.Entries[0].State != RunRecoveryUnverifiable || summary.Entries[0].Wrote {
		t.Fatalf("probe error summary = %+v", summary.Entries)
	}
	// Unverifiable never masquerades as gone: the run stays running.
	run, _ := db.RunGet(ctx, "run-err")
	if run.Status != store.RunStatusRunning || run.FinishedAt != nil {
		t.Fatalf("probe error wrote the run: %+v", run)
	}
}

func TestRunRecoveryReplacedWithoutIncarnationIsUnverifiable(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	insertRun(t, db, ctx, "run-replaced", conversationID, store.RunStatusRunning, false)
	probe := newFakeRunProbe()
	probe.set("run-replaced", RosterRuntimeReplaced, "")

	summary, err := NewRunRecovery(db).Recover(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != 1 || summary.Entries[0].State != RunRecoveryUnverifiable || summary.Entries[0].Wrote {
		t.Fatalf("empty incarnation summary = %+v", summary.Entries)
	}
	if summary.Unverifiable != 1 || summary.Replaced != 0 {
		t.Fatalf("counts = %+v", summary)
	}
	// The row is untouched: no terminal write without authoritative evidence.
	run, _ := db.RunGet(ctx, "run-replaced")
	if run.Status != store.RunStatusRunning || run.FinishedAt != nil {
		t.Fatalf("empty incarnation wrote the run: %+v", run)
	}
}

// gateRunProbe blocks its first probe call until released, forcing the
// exact recovery-vs-concurrent-finish interleaving.
type gateRunProbe struct {
	status  RosterRuntimeStatus
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *gateRunProbe) ProbeRunRuntime(context.Context, string) (RosterRuntimeStatus, error) {
	p.once.Do(func() { close(p.entered); <-p.release })
	return p.status, nil
}

func TestRunRecoveryConcurrentFinishIsNotCountedAsWrite(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	insertRun(t, db, ctx, "run-race", conversationID, store.RunStatusRunning, false)
	probe := &gateRunProbe{
		status:  RosterRuntimeStatus{Kind: RosterRuntimeGone},
		entered: make(chan struct{}), release: make(chan struct{}),
	}

	done := make(chan RunRecoverySummary, 1)
	go func() {
		summary, err := NewRunRecovery(db).Recover(ctx, probe)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	// Recovery is blocked inside the probe; a concurrent writer finishes
	// the run with a fresh answer first.
	<-probe.entered
	if err := db.RunFinishUsage(ctx, "run-race", store.RunStatusCompleted, "fresh answer", "", 1, 1, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	summary := <-done

	if len(summary.Entries) != 1 {
		t.Fatalf("summary = %+v", summary.Entries)
	}
	entry := summary.Entries[0]
	if entry.State != RunRecoveryGone || entry.Wrote {
		t.Fatalf("concurrent finish accounted as a write: %+v", entry)
	}
	if !strings.Contains(entry.Reason, "already finished") || !strings.Contains(entry.Reason, store.RunStatusCompleted) {
		t.Fatalf("no-op reason hides the existing terminal state: %q", entry.Reason)
	}
	// The concurrent writer's terminal state stands.
	run, _ := db.RunGet(ctx, "run-race")
	if run.Status != store.RunStatusCompleted || run.Answer != "fresh answer" || run.FinishedAt == nil {
		t.Fatalf("concurrent finish was overwritten: %+v", run)
	}
}

func TestRunRecoveryStaleResumeIsNotTerminallyWritten(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	insertRun(t, db, ctx, "run-resume", conversationID, store.RunStatusInterrupted, false)
	probe := &gateRunProbe{
		status:  RosterRuntimeStatus{Kind: RosterRuntimeGone},
		entered: make(chan struct{}), release: make(chan struct{}),
	}

	done := make(chan RunRecoverySummary, 1)
	go func() {
		summary, err := NewRunRecovery(db).Recover(ctx, probe)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	// Recovery is blocked inside the probe; a HITL resume moves the run
	// back to running before the stale probe answer lands.
	<-probe.entered
	if err := db.RunUpdateStatus(ctx, "run-resume", store.RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	summary := <-done

	if len(summary.Entries) != 1 {
		t.Fatalf("summary = %+v", summary.Entries)
	}
	entry := summary.Entries[0]
	// The stale probe must not terminally write the resumed run.
	if entry.Wrote {
		t.Fatalf("stale probe wrote the resumed run: %+v", entry)
	}
	// The reason must report a still-active changed-after-scan no-op, not
	// a finished terminal state: the resumed run never finished.
	if !strings.Contains(entry.Reason, "still active") || !strings.Contains(entry.Reason, store.RunStatusRunning) {
		t.Fatalf("stale-resume reason misreports the outcome: %q", entry.Reason)
	}
	if strings.Contains(entry.Reason, "already finished") {
		t.Fatalf("stale-resume reason claims a finish that never happened: %q", entry.Reason)
	}
	run, _ := db.RunGet(ctx, "run-resume")
	if run.Status != store.RunStatusRunning || run.FinishedAt != nil {
		t.Fatalf("resumed run was terminally written: %+v", run)
	}
}

func TestRunRecoveryUnfinishedOnlySkipsFinishedHistory(t *testing.T) {
	db, ctx, conversationID := recoveryFixture(t)
	// Retained finished interrupted history, as recovery's own exited
	// transition persists it, plus genuinely active runs.
	for i := 0; i < 5; i++ {
		insertRun(t, db, ctx, fmt.Sprintf("run-old-%d", i), conversationID, store.RunStatusInterrupted, true)
	}
	insertRun(t, db, ctx, "run-active-1", conversationID, store.RunStatusRunning, false)
	insertRun(t, db, ctx, "run-active-2", conversationID, store.RunStatusInterrupted, false)
	probe := newFakeRunProbe()
	probe.set("run-active-1", RosterRuntimeConnected, "")
	probe.set("run-active-2", RosterRuntimeGone, "")

	summary, err := NewRunRecovery(db).Recover(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Entries) != 2 {
		t.Fatalf("finished history was refetched: %+v", summary.Entries)
	}
	for _, entry := range summary.Entries {
		if entry.RunID != "run-active-1" && entry.RunID != "run-active-2" {
			t.Fatalf("unexpected entry %+v", entry)
		}
	}
	// The finished interrupted history is untouched.
	for i := 0; i < 5; i++ {
		run, err := db.RunGet(ctx, fmt.Sprintf("run-old-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != store.RunStatusInterrupted || run.FinishedAt == nil {
			t.Fatalf("finished history touched: %+v", run)
		}
	}
}
