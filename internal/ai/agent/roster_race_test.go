package agent

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestRosterConcurrentProvisionSameHandle(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	roster := newTestRoster(fake, newFakeRosterProbe())

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", nil)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRosterHandleConflict):
			conflicted++
		default:
			t.Fatalf("unexpected provision error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != workers-1 {
		t.Fatalf("succeeded=%d conflicted=%d, want 1/%d", succeeded, conflicted, workers-1)
	}
	rows, err := fake.AgentRosterList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("store holds %d rows, want exactly 1", len(rows))
	}
}

func TestRosterConcurrentAttachBindsLatestJob(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(fake, probe)
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", nil); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(job int) {
			defer wg.Done()
			<-start
			attachment, err := roster.Attach(ctx, "handle-a", fmt.Sprintf("job-%d", job))
			if err == nil && attachment.Resolution != RosterResolutionConnected {
				err = fmt.Errorf("resolution=%s", attachment.Resolution)
			}
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(RosterStateActive) {
		t.Fatalf("state=%s", stored.State)
	}
	bound := false
	for i := 0; i < workers; i++ {
		if stored.JobID == fmt.Sprintf("job-%d", i) {
			bound = true
		}
	}
	if !bound {
		t.Fatalf("job binding %q is not one of the attaching jobs", stored.JobID)
	}
}

func TestRosterConcurrentReleaseStaysValid(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(fake, probe)
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	type releaseResult struct {
		confirm bool
		err     error
	}
	results := make(chan releaseResult, 3)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry))
		results <- releaseResult{err: err}
	}()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := roster.ConfirmRelease(ctx, "handle-a", rosterIdentityOf(entry))
			results <- releaseResult{confirm: true, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	confirmed := 0
	for result := range results {
		if result.err == nil {
			if result.confirm {
				confirmed++
			}
			continue
		}
		if !result.confirm || !errors.Is(result.err, ErrRosterInvalidTransition) {
			t.Fatalf("unexpected release error: %v", result.err)
		}
	}
	if confirmed > 1 {
		t.Fatalf("%d confirmations succeeded, want at most one", confirmed)
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	switch RosterState(stored.State) {
	case RosterStateReleasePending:
		if stored.ReleasedAt != nil {
			t.Fatal("release-pending row carries a release stamp")
		}
	case RosterStateReleased:
		if stored.ReleasedAt == nil {
			t.Fatal("released row has no release stamp")
		}
	default:
		t.Fatalf("concurrent release produced invalid state %s", stored.State)
	}
}

func TestRosterConcurrentAttachAndReconcile(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(fake, probe)
	if _, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", nil); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := roster.Attach(ctx, "handle-a", "job-1")
		errs <- err
	}()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := roster.Reconcile(ctx, "conv-1")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	// The connected runtime is never an orphan: reconcile must not write it away.
	if stored.State != string(RosterStateActive) && stored.State != string(RosterStateProvisioning) {
		t.Fatalf("state after concurrent attach/reconcile = %s", stored.State)
	}
}

// Concurrent renames race with a lifecycle transition: the alias CAS never
// disturbs the state machine, every writer with the current identity lands
// exactly one final alias, and stale identities always conflict.
func TestRosterConcurrentRenameAndLifecycle(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(fake, probe)
	entry, err := roster.Provision(ctx, "conv-1", "handle-a", "", "tab-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Activate(ctx, entry.ID, rosterIdentityOf(entry), "job-0"); err != nil {
		t.Fatal(err)
	}

	const renamers = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, renamers+2)
	for i := 0; i < renamers; i++ {
		wg.Add(1)
		go func(alias int) {
			defer wg.Done()
			<-start
			renamed, err := roster.Rename(ctx, "handle-a", rosterIdentityOf(entry), fmt.Sprintf("alias-%d", alias))
			if err == nil && renamed.Handle != "handle-a" {
				err = fmt.Errorf("handle changed to %q", renamed.Handle)
			}
			errs <- err
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := roster.RequestRelease(ctx, "handle-a", rosterIdentityOf(entry))
		errs <- err
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		// A stale identity must conflict no matter when it lands.
		_, err := roster.Rename(ctx, "handle-a", RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: "inc-stale"}, "alias-stale")
		if errors.Is(err, ErrRosterIdentityConflict) {
			err = nil
		}
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored, err := fake.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	aliasOK := false
	for i := 0; i < renamers; i++ {
		if stored.Alias == fmt.Sprintf("alias-%d", i) {
			aliasOK = true
		}
	}
	if !aliasOK {
		t.Fatalf("final alias %q is not one of the concurrent renames", stored.Alias)
	}
	switch RosterState(stored.State) {
	case RosterStateActive, RosterStateReleasePending:
	default:
		t.Fatalf("concurrent rename/release produced invalid state %s", stored.State)
	}
	if stored.Incarnation != entry.Incarnation || stored.Handle != "handle-a" {
		t.Fatalf("concurrent renames disturbed the durable identity: %+v", stored)
	}
}
