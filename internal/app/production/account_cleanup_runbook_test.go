package production

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// fakeRunbookCleanup tracks live runs per owner and cancels the prepared
// snapshot on commit; rollback is a no-op, mirroring the real contract.
type fakeRunbookCleanup struct {
	mu        sync.Mutex
	live      map[string][]string
	canceled  map[string][]string
	prepares  int
	commits   int
	rollbacks int
}

func newFakeRunbookCleanup() *fakeRunbookCleanup {
	return &fakeRunbookCleanup{live: map[string][]string{}, canceled: map[string][]string{}}
}

func (f *fakeRunbookCleanup) PrepareOwnerCleanup(_ context.Context, ownerID, _ string, _ *time.Time) (func(...string), func()) {
	f.mu.Lock()
	f.prepares++
	snapshot := append([]string(nil), f.live[ownerID]...)
	f.mu.Unlock()
	var once sync.Once
	commit := func(...string) {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.commits++
			for _, id := range snapshot {
				f.canceled[ownerID] = append(f.canceled[ownerID], id)
				for index, liveID := range f.live[ownerID] {
					if liveID == id {
						f.live[ownerID] = append(f.live[ownerID][:index], f.live[ownerID][index+1:]...)
						break
					}
				}
			}
		})
	}
	rollback := func() {
		f.mu.Lock()
		f.rollbacks++
		f.mu.Unlock()
	}
	return commit, rollback
}

func (f *fakeRunbookCleanup) canceledRuns(ownerID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.canceled[ownerID]...)
}

func TestAccountCleanupCancelsOwnerRunbookRunsOnly(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1", "run-a2"}
	runbook.live["owner-b"] = []string{"run-b1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)

	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-a", "logout-a"); err != nil {
		t.Fatal(err)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 2 || got[0] != "run-a1" || got[1] != "run-a2" {
		t.Fatalf("owner A canceled runs = %v, want both of A's live runs in order", got)
	}
	if got := runbook.canceledRuns("owner-b"); len(got) != 0 {
		t.Fatalf("owner B runs canceled by A's cleanup: %v", got)
	}
	if got := runbook.live["owner-b"]; len(got) != 1 || got[0] != "run-b1" {
		t.Fatalf("owner B live runs changed: %v", got)
	}
	if runbook.commits != 1 || runbook.rollbacks != 0 {
		t.Fatalf("runbook commits = %d, rollbacks = %d, want 1 and 0", runbook.commits, runbook.rollbacks)
	}
}

func TestAccountCleanupRunbookCommitOnlyAtSnapshotCommit(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)
	ownerA := ipcWithUser("owner-a")
	if _, tab := rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice}); tab.ID == "" {
		t.Fatal("device tab not created")
	}

	_, rollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "logout-a")
	if err != nil {
		t.Fatal(err)
	}
	if runbook.commits != 0 {
		t.Fatal("runbook committed at prepare time, not at the snapshot commit boundary")
	}
	rollback()
	if runbook.commits != 0 || runbook.rollbacks != 1 {
		t.Fatalf("after rollback: commits = %d, rollbacks = %d, want 0 and 1", runbook.commits, runbook.rollbacks)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 0 {
		t.Fatalf("rollback canceled runbook runs: %v", got)
	}

	commit2, _, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "logout-a")
	if err != nil {
		t.Fatal(err)
	}
	commit2("logout-a")
	if runbook.commits != 1 {
		t.Fatalf("runbook commits = %d, want 1 after the snapshot commit", runbook.commits)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 1 || got[0] != "run-a1" {
		t.Fatalf("owner A canceled runs = %v, want the snapshot's live run", got)
	}
}

func TestAccountCleanupSnapshotFailureKeepsRunbookRuns(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)
	application.accountCleanup.listOwnerSessions = func(context.Context, string) ([]session.SessionInfo, error) {
		return nil, errors.New("injected listing failure")
	}

	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-a", "logout-a"); err == nil {
		t.Fatal("CleanupAccount swallowed the snapshot read failure")
	}
	if runbook.commits != 0 || runbook.rollbacks != 1 {
		t.Fatalf("after failed snapshot: commits = %d, rollbacks = %d, want 0 and 1", runbook.commits, runbook.rollbacks)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 0 {
		t.Fatalf("failed snapshot canceled runbook runs: %v", got)
	}

	// Same-event retry after the read recovers commits and cancels.
	application.accountCleanup.listOwnerSessions = nil
	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-a", "logout-a"); err != nil {
		t.Fatal(err)
	}
	if runbook.commits != 1 {
		t.Fatalf("retry commits = %d, want 1", runbook.commits)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 1 || got[0] != "run-a1" {
		t.Fatalf("retry canceled runs = %v, want the snapshot's live run", got)
	}
}

func TestAccountCleanupDeferredRunbookEpochBoundary(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)
	ownerA := ipcWithUser("owner-a")
	connected, tab := rig.open(t, ownerA, session.Asset{ID: "device/a", Kind: session.KindDevice})

	// The deferred (natural-expiry) path runs the same commit boundary: the
	// runbook run is canceled only when the cleanup event commits.
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a"); err != nil {
		t.Fatal(err)
	}
	if runbook.commits != 1 {
		t.Fatalf("deferred cleanup commits = %d, want 1", runbook.commits)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 1 || got[0] != "run-a1" {
		t.Fatalf("deferred cleanup canceled runs = %v, want the snapshot's live run", got)
	}
	requireAccountCleanupTab(t, ownerA, rig.manager, tab.ID, false)
	requireAccountCleanupStatus(t, ownerA, rig.manager, connected.ID, session.StatusDisconnected)
}

func ipcWithUser(userID string) context.Context {
	return ipc.WithUserID(context.Background(), userID)
}

// A replay of an already-committed cleanup event (directly or through a
// registered alias) reuses the original snapshot and must not cancel a new
// generation of runbook runs started after the first commit.
func TestAccountCleanupReplayDoesNotCancelNewRunbookGeneration(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)

	commit, _, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "logout-all:login-1")
	if err != nil {
		t.Fatal(err)
	}
	commit("login-1", "login-2")
	if got := runbook.canceledRuns("owner-a"); len(got) != 1 || got[0] != "run-a1" {
		t.Fatalf("first commit canceled runs = %v, want the original snapshot's run", got)
	}

	// A new generation starts after the commit.
	runbook.mu.Lock()
	runbook.live["owner-a"] = append(runbook.live["owner-a"], "run-b")
	runbook.mu.Unlock()

	// Replaying through the registered alias must reuse the original snapshot.
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-2", "login-2"); err != nil {
		t.Fatal(err)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 1 || got[0] != "run-a1" {
		t.Fatalf("alias replay canceled runs = %v, want only the original snapshot's run (no new generation)", got)
	}
	if got := runbook.live["owner-a"]; len(got) != 1 || got[0] != "run-b" {
		t.Fatalf("alias replay canceled the new generation: live = %v, want run-b to survive", got)
	}

	// Replaying the original event id likewise reuses the original snapshot.
	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-all:login-1", "login-1"); err != nil {
		t.Fatal(err)
	}
	if got := runbook.live["owner-a"]; len(got) != 1 || got[0] != "run-b" {
		t.Fatalf("event replay canceled the new generation: live = %v, want run-b to survive", got)
	}
	if runbook.commits != 1 {
		t.Fatalf("runbook snapshot taken %d times, want exactly once per canonical event", runbook.commits)
	}
}

// A rollback or a snapshot-read-failure leaves the event absent, so a
// same-event retry takes a fresh snapshot and cancels normally.
func TestAccountCleanupRetryAfterRollbackResnapshotsRunbook(t *testing.T) {
	rig := newAccountCleanupRig(t)
	runbook := newFakeRunbookCleanup()
	runbook.live["owner-a"] = []string{"run-a1"}
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.SetRunbookCleanup(runbook)

	commit, rollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "logout-a")
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	if runbook.commits != 0 || runbook.rollbacks != 1 {
		t.Fatalf("after rollback: commits = %d, rollbacks = %d, want 0 and 1", runbook.commits, runbook.rollbacks)
	}
	_ = commit

	// The same event retries with a fresh snapshot and cancels.
	runbook.mu.Lock()
	runbook.live["owner-a"] = append(runbook.live["owner-a"], "run-a2")
	runbook.mu.Unlock()
	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-a", "logout-a"); err != nil {
		t.Fatal(err)
	}
	if got := runbook.canceledRuns("owner-a"); len(got) != 2 || got[0] != "run-a1" || got[1] != "run-a2" {
		t.Fatalf("retry after rollback canceled runs = %v, want the fresh snapshot's runs", got)
	}
}
