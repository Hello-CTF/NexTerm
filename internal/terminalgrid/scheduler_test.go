package terminalgrid_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	grid "github.com/ProbiusOfficial/NexTerm/internal/terminalgrid"
)

func TestSerializedLatestWinsWithBoundedPendingState(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport.resize})
	firstGrid := grid.Grid{Cols: 60, Rows: 20}
	superseded := grid.Grid{Cols: 75, Rows: 25}
	latest := grid.Grid{Cols: 110, Rows: 35}

	mustSetDesired(t, model, firstGrid)
	first := transport.next(t)
	mustSetDesired(t, model, superseded)
	latestRevision := mustSetDesired(t, model, latest)
	snapshot := model.Snapshot()
	if !snapshot.InFlight || !snapshot.Pending || snapshot.DesiredGrid != latest || snapshot.Revision != latestRevision {
		t.Fatalf("scheduled state = %+v", snapshot)
	}
	transport.assertNoCall(t)

	first.finish(nil)
	second := transport.next(t)
	if second.grid != latest {
		t.Fatalf("second request = %+v, want latest %+v", second.grid, latest)
	}
	second.finish(nil)
	waitFor(t, func() bool {
		state := model.Snapshot()
		return state.CommittedGrid == latest && state.LastGoodGrid == latest && !state.InFlight && !state.Pending
	})
	transport.assertNoCall(t)
}

func TestTransportFailureDoesNotCommitAndIsPropagated(t *testing.T) {
	transportErr := errors.New("transport rejected window change")
	results := make(chan grid.Result, 8)
	transport := func(_ context.Context, desired grid.Grid) error {
		if desired.Cols == 100 {
			return transportErr
		}
		return nil
	}
	model := newModel(t, grid.Config{
		Mode:     grid.ModeController,
		Resize:   transport,
		OnResult: func(result grid.Result) { results <- result },
	})
	committed := grid.Grid{Cols: 90, Rows: 30}
	mustSetDesired(t, model, committed)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == committed })

	mustSetDesired(t, model, grid.Grid{Cols: 100, Rows: 40})
	if err := model.Flush(context.Background()); !errors.Is(err, transportErr) {
		t.Fatalf("Flush error = %v, want %v", err, transportErr)
	}
	var resizeErr *grid.ResizeError
	if err := model.Flush(context.Background()); !errors.As(err, &resizeErr) || resizeErr.Grid.Cols != 100 {
		t.Fatalf("typed Flush error = %#v", err)
	}
	snapshot := model.Snapshot()
	if snapshot.CommittedGrid != committed || snapshot.LastGoodGrid != committed {
		t.Fatalf("failed transport changed committed state: %+v", snapshot)
	}
	for len(results) > 0 {
		result := <-results
		if result.Grid.Cols == 100 {
			if !errors.Is(result.Err, transportErr) || result.Committed {
				t.Fatalf("failed result = %+v", result)
			}
		}
	}
}

func TestFlushForcesFinalRequestAndHonorsCallerCancellation(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport.resize})
	desired := grid.Grid{Cols: 80, Rows: 30}
	mustSetDesired(t, model, desired)
	first := transport.next(t)
	first.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == desired })
	before := model.Snapshot().Revision

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := model.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Flush error = %v", err)
	}
	transport.assertNoCall(t)

	flushed := make(chan error, 1)
	go func() { flushed <- model.Flush(context.Background()) }()
	final := transport.next(t)
	if final.grid != desired {
		t.Fatalf("final grid = %+v", final.grid)
	}
	final.finish(nil)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if model.Snapshot().Revision <= before {
		t.Fatal("final flush did not allocate a monotonic revision")
	}

	mustSetDesired(t, model, grid.Grid{Cols: 81, Rows: 31})
	inFlight := transport.next(t)
	waitCtx, stopWaiting := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() { waitResult <- model.Flush(waitCtx) }()
	stopWaiting()
	if err := <-waitResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation = %v", err)
	}
	inFlight.finish(nil)
}

func TestReattachIgnoresDelayedOldTransportSuccess(t *testing.T) {
	oldTransport := newControlledTransport()
	newTransport := newControlledTransport()
	results := make(chan grid.Result, 4)
	model := newModel(t, grid.Config{
		Mode:     grid.ModeController,
		Resize:   oldTransport.resize,
		OnResult: func(result grid.Result) { results <- result },
	})
	desired := grid.Grid{Cols: 105, Rows: 33}
	mustSetDesired(t, model, desired)
	oldCall := oldTransport.next(t)

	if err := model.Reattach(newTransport.resize); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldCall.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("reattach did not cancel old transport")
	}
	snapshot := model.Snapshot()
	if snapshot.CommittedGrid.Valid() || !snapshot.Pending || !snapshot.Attached {
		t.Fatalf("reattach state = %+v", snapshot)
	}
	newTransport.assertNoCall(t)

	oldCall.finish(nil)
	oldResult := <-results
	if oldResult.Err != nil || oldResult.Committed {
		t.Fatalf("late old result = %+v", oldResult)
	}
	newCall := newTransport.next(t)
	if newCall.grid != desired {
		t.Fatalf("new attachment grid = %+v", newCall.grid)
	}
	if model.Snapshot().CommittedGrid.Valid() {
		t.Fatal("late old success populated the new attachment's committed grid")
	}
	newCall.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == desired })
}

func TestClaimFencesInflightAndForcesLatest(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport.resize})
	desired := grid.Grid{Cols: 88, Rows: 28}
	mustSetDesired(t, model, desired)
	beforeClaim := transport.next(t)
	if err := model.Claim(); err != nil {
		t.Fatal(err)
	}
	beforeClaim.finish(nil)
	afterClaim := transport.next(t)
	if afterClaim.grid != desired {
		t.Fatalf("claim retry = %+v", afterClaim.grid)
	}
	if model.Snapshot().CommittedGrid.Valid() {
		t.Fatal("pre-claim completion crossed the claim fence")
	}
	afterClaim.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == desired })
}

func TestDisconnectedIntentSurvivesUntilReattach(t *testing.T) {
	model := newModel(t, grid.Config{Mode: grid.ModeController})
	desired := grid.Grid{Cols: 97, Rows: 29}
	mustSetDesired(t, model, desired)
	if err := model.Flush(context.Background()); !errors.Is(err, grid.ErrDisconnected) {
		t.Fatalf("disconnected Flush error = %v", err)
	}
	if !model.Snapshot().Pending {
		t.Fatal("latest disconnected intent was not retained")
	}
	transport := newControlledTransport()
	if err := model.Reattach(transport.resize); err != nil {
		t.Fatal(err)
	}
	call := transport.next(t)
	if call.grid != desired {
		t.Fatalf("reattach flushed %+v", call.grid)
	}
	call.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == desired })
}

func TestOnResultMayReenterModel(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	var model *grid.Model
	transport := func(context.Context, grid.Grid) error { return nil }
	model = newModel(t, grid.Config{
		Mode:   grid.ModeController,
		Resize: transport,
		OnResult: func(grid.Result) {
			_ = model.Snapshot()
			wg.Done()
		},
	})
	mustSetDesired(t, model, grid.Grid{Cols: 80, Rows: 24})
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("result callback deadlocked on model state")
	}
}
