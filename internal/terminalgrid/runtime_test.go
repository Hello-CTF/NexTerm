package terminalgrid_test

import (
	"context"
	"sync/atomic"
	"testing"

	grid "github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func TestInitialGridAndWaitAvoidDuplicateTransport(t *testing.T) {
	var calls atomic.Int32
	model := newModel(t, grid.Config{
		Mode:        grid.ModeController,
		InitialGrid: grid.Grid{Cols: 80, Rows: 24},
		Resize: func(context.Context, grid.Grid) error {
			calls.Add(1)
			return nil
		},
	})
	initial := grid.Grid{Cols: 80, Rows: 24}
	snapshot := model.Snapshot()
	if snapshot.DesiredGrid != initial || snapshot.CommittedGrid != initial || snapshot.LastGoodGrid != initial {
		t.Fatalf("initial state = %+v", snapshot)
	}
	if err := model.Wait(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	revision := mustSetDesired(t, model, initial)
	if revision != 0 {
		t.Fatalf("unchanged initial revision = %d", revision)
	}
	if err := model.Wait(context.Background(), revision); err != nil {
		t.Fatal(err)
	}
	assertCallCount(t, &calls, 0)

	desired := grid.Grid{Cols: 100, Rows: 40}
	revision = mustSetDesired(t, model, desired)
	if err := model.Wait(context.Background(), revision); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot().CommittedGrid; got != desired {
		t.Fatalf("committed = %+v, want %+v", got, desired)
	}
	assertCallCount(t, &calls, 1)
	if err := model.Wait(context.Background(), revision+1); err == nil {
		t.Fatal("Wait accepted an unsubmitted revision")
	}
}

func TestCurrentRejectsClaimAndReattachFences(t *testing.T) {
	oldTransport := newControlledTransport()
	newTransport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: oldTransport.resize})
	if model.Current(context.Background()) {
		t.Fatal("ordinary context matched a resize request")
	}
	mustSetDesired(t, model, grid.Grid{Cols: 90, Rows: 30})
	oldCall := oldTransport.next(t)
	if !model.Current(oldCall.ctx) {
		t.Fatal("active request was not current")
	}
	if err := model.Reattach(newTransport.resize); err != nil {
		t.Fatal(err)
	}
	if model.Current(oldCall.ctx) {
		t.Fatal("reattached request remained current")
	}
	oldCall.finish(nil)
	newCall := newTransport.next(t)
	if !model.Current(newCall.ctx) {
		t.Fatal("replacement request was not current")
	}
	if err := model.Claim(); err != nil {
		t.Fatal(err)
	}
	if model.Current(newCall.ctx) {
		t.Fatal("claimed request remained current")
	}
	newCall.finish(nil)
	finalCall := newTransport.next(t)
	finalCall.finish(nil)
}
