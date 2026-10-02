package terminalgrid_test

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	grid "github.com/ProbiusOfficial/NexTerm/internal/terminalgrid"
)

func TestGridForViewport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		viewport grid.Viewport
		metrics  grid.CellMetrics
		want     grid.Grid
		ok       bool
		wantErr  bool
	}{
		{name: "floor", viewport: grid.Viewport{WidthPx: 1009, HeightPx: 519}, metrics: grid.CellMetrics{WidthPx: 10, HeightPx: 20}, want: grid.Grid{Cols: 100, Rows: 25}, ok: true},
		{name: "fractional cells", viewport: grid.Viewport{WidthPx: 100, HeightPx: 50}, metrics: grid.CellMetrics{WidthPx: 7.5, HeightPx: 12.5}, want: grid.Grid{Cols: 13, Rows: 4}, ok: true},
		{name: "positive tiny viewport", viewport: grid.Viewport{WidthPx: 1, HeightPx: 1}, metrics: grid.CellMetrics{WidthPx: 10, HeightPx: 20}, want: grid.Grid{Cols: 1, Rows: 1}, ok: true},
		{name: "dimension cap", viewport: grid.Viewport{WidthPx: 20000, HeightPx: 30000}, metrics: grid.CellMetrics{WidthPx: 10, HeightPx: 20}, want: grid.Grid{Cols: 1024, Rows: 1024}, ok: true},
		{name: "zero viewport", viewport: grid.Viewport{}, metrics: grid.CellMetrics{}, ok: false},
		{name: "negative viewport", viewport: grid.Viewport{WidthPx: -1, HeightPx: 100}, metrics: grid.CellMetrics{}, ok: false},
		{name: "zero metrics", viewport: grid.Viewport{WidthPx: 100, HeightPx: 100}, metrics: grid.CellMetrics{WidthPx: 0, HeightPx: 10}, wantErr: true},
		{name: "nan metrics", viewport: grid.Viewport{WidthPx: 100, HeightPx: 100}, metrics: grid.CellMetrics{WidthPx: math.NaN(), HeightPx: 10}, wantErr: true},
		{name: "infinite metrics", viewport: grid.Viewport{WidthPx: 100, HeightPx: 100}, metrics: grid.CellMetrics{WidthPx: 10, HeightPx: math.Inf(1)}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := grid.GridForViewport(test.viewport, test.metrics)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want || ok != test.ok {
				t.Fatalf("GridForViewport() = %+v, %v; want %+v, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestHiddenNeverInventsOrSendsAGrid(t *testing.T) {
	var calls atomic.Int32
	transport := func(context.Context, grid.Grid) error {
		calls.Add(1)
		return nil
	}
	model := newModel(t, grid.Config{Mode: grid.ModeHidden, Resize: transport})

	if _, err := model.SetViewport(grid.Viewport{}, grid.CellMetrics{}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.SetViewport(
		grid.Viewport{WidthPx: 913, HeightPx: 477},
		grid.CellMetrics{WidthPx: 10, HeightPx: 20},
	); err != nil {
		t.Fatal(err)
	}
	if err := model.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := model.Flush(context.Background()); !errors.Is(err, grid.ErrHidden) {
		t.Fatalf("hidden Flush error = %v", err)
	}
	assertCallCount(t, &calls, 0)
	snapshot := model.Snapshot()
	if snapshot.DesiredGrid != (grid.Grid{Cols: 91, Rows: 23}) {
		t.Fatalf("desired = %+v", snapshot.DesiredGrid)
	}
	if snapshot.CommittedGrid.Valid() || snapshot.LastGoodGrid.Valid() {
		t.Fatalf("hidden model invented committed state: %+v", snapshot)
	}

	if _, err := model.SetViewport(grid.Viewport{}, grid.CellMetrics{}); err != nil {
		t.Fatal(err)
	}
	if model.Snapshot().DesiredGrid != (grid.Grid{Cols: 91, Rows: 23}) {
		t.Fatal("zero viewport replaced the last valid desired grid")
	}
	if err := model.SetMode(grid.ModeController); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == (grid.Grid{Cols: 91, Rows: 23}) })
	assertCallCount(t, &calls, 1)

	if err := model.SetMode(grid.ModeHidden); err != nil {
		t.Fatal(err)
	}
	if err := model.SetMode(grid.ModeController); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return calls.Load() == 2 })
	assertCallCount(t, &calls, 2)
}

func TestHiddenTinyViewportDoesNotLeakOneCellGridOnResume(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport.resize})
	valid := grid.Grid{Cols: 93, Rows: 27}
	mustSetDesired(t, model, valid)
	initial := transport.next(t)
	initial.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == valid })

	if err := model.SetMode(grid.ModeHidden); err != nil {
		t.Fatal(err)
	}
	beforeTiny := model.Snapshot().Revision
	tiny := grid.Viewport{WidthPx: 1, HeightPx: 1}
	if _, err := model.SetViewport(tiny, grid.CellMetrics{WidthPx: 10, HeightPx: 20}); err != nil {
		t.Fatal(err)
	}
	hidden := model.Snapshot()
	if hidden.ViewportPx != tiny {
		t.Fatalf("hidden viewport = %+v, want %+v", hidden.ViewportPx, tiny)
	}
	if hidden.DesiredGrid != valid || hidden.Revision != beforeTiny {
		t.Errorf("hidden tiny measurement changed intent: %+v", hidden)
	}
	transport.assertNoCall(t)

	if err := model.SetMode(grid.ModeController); err != nil {
		t.Fatal(err)
	}
	resumed := transport.next(t)
	resumedGrid := resumed.grid
	resumed.finish(nil)
	if resumedGrid != valid {
		t.Fatalf("resume transport grid = %+v, want preserved %+v; no 1x1 request is allowed", resumedGrid, valid)
	}
	transport.assertNoCall(t)
}

func TestHiddenCancelsWorkAndResumeFlushesLatest(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport.resize})

	initial := grid.Grid{Cols: 70, Rows: 20}
	intermediate := grid.Grid{Cols: 90, Rows: 30}
	final := grid.Grid{Cols: 100, Rows: 35}
	mustSetDesired(t, model, initial)
	first := transport.next(t)
	first.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == initial })

	mustSetDesired(t, model, intermediate)
	inFlight := transport.next(t)
	mustSetDesired(t, model, final)
	if err := model.SetMode(grid.ModeHidden); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inFlight.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("hiding did not cancel the in-flight request")
	}
	inFlight.finish(nil) // A late success must not commit after the mode fence.
	transport.assertNoCall(t)
	snapshot := model.Snapshot()
	if snapshot.CommittedGrid.Valid() || snapshot.LastGoodGrid != initial || snapshot.DesiredGrid != final {
		t.Fatalf("hidden state = %+v", snapshot)
	}
	if _, err := model.SetViewport(grid.Viewport{}, grid.CellMetrics{}); err != nil {
		t.Fatal(err)
	}
	transport.assertNoCall(t)

	if err := model.SetMode(grid.ModeController); err != nil {
		t.Fatal(err)
	}
	resumed := transport.next(t)
	if resumed.grid != final {
		t.Fatalf("resume grid = %+v, want %+v", resumed.grid, final)
	}
	resumed.finish(nil)
	waitFor(t, func() bool {
		state := model.Snapshot()
		return state.CommittedGrid == final && state.LastGoodGrid == final
	})
}

func TestObserverFiltersStaleRevisionsAndClaimUsesLocalIntent(t *testing.T) {
	transport := newControlledTransport()
	model := newModel(t, grid.Config{Mode: grid.ModeObserver, Resize: transport.resize})
	if _, err := model.SetViewport(
		grid.Viewport{WidthPx: 850, HeightPx: 630},
		grid.CellMetrics{WidthPx: 10, HeightPx: 20},
	); err != nil {
		t.Fatal(err)
	}
	localRevision := model.Snapshot().Revision

	remote := grid.Grid{Cols: 100, Rows: 40}
	accepted, err := model.Observe(10, remote)
	if err != nil || !accepted {
		t.Fatalf("Observe() = %v, %v", accepted, err)
	}
	for _, observation := range []struct {
		revision uint64
		grid     grid.Grid
	}{
		{revision: 9, grid: grid.Grid{Cols: 1, Rows: 1}},
		{revision: 10, grid: grid.Grid{Cols: 80, Rows: 24}},
	} {
		accepted, err := model.Observe(observation.revision, observation.grid)
		if err != nil || accepted {
			t.Fatalf("stale Observe(%d) = %v, %v", observation.revision, accepted, err)
		}
	}
	if err := model.Flush(context.Background()); !errors.Is(err, grid.ErrNotController) {
		t.Fatalf("observer Flush error = %v", err)
	}
	transport.assertNoCall(t)

	if err := model.Claim(); err != nil {
		t.Fatal(err)
	}
	claim := transport.next(t)
	wantLocal := grid.Grid{Cols: 85, Rows: 31}
	if claim.grid != wantLocal {
		t.Fatalf("claim grid = %+v, want local %+v", claim.grid, wantLocal)
	}
	claim.finish(nil)
	waitFor(t, func() bool { return model.Snapshot().CommittedGrid == wantLocal })
	snapshot := model.Snapshot()
	if snapshot.LastGoodGrid != wantLocal || snapshot.ObservedRevision != 10 || snapshot.Revision <= localRevision {
		t.Fatalf("claimed state = %+v", snapshot)
	}
}

func TestObserverReattachAcceptsOnlyCurrentOrNewerReplay(t *testing.T) {
	model := newModel(t, grid.Config{Mode: grid.ModeObserver})
	first := grid.Grid{Cols: 90, Rows: 30}
	if accepted, err := model.Observe(7, first); err != nil || !accepted {
		t.Fatalf("Observe() = %v, %v", accepted, err)
	}
	if err := model.Reattach(nil); err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot()
	if snapshot.CommittedGrid.Valid() || snapshot.LastGoodGrid != first {
		t.Fatalf("reattached observer state = %+v", snapshot)
	}
	if accepted, err := model.Observe(6, grid.Grid{Cols: 1, Rows: 1}); err != nil || accepted {
		t.Fatalf("old replay = %v, %v", accepted, err)
	}
	if accepted, err := model.Observe(7, grid.Grid{Cols: 91, Rows: 31}); err != nil || accepted {
		t.Fatalf("conflicting replay = %v, %v", accepted, err)
	}
	if accepted, err := model.Observe(7, first); err != nil || !accepted {
		t.Fatalf("identical replay = %v, %v", accepted, err)
	}
	if accepted, err := model.Observe(7, first); err != nil || accepted {
		t.Fatalf("duplicate replay = %v, %v", accepted, err)
	}
}

func TestObserverRevisionIsIndependentOfLocalIntent(t *testing.T) {
	model := newModel(t, grid.Config{Mode: grid.ModeObserver})
	for i := 0; i < 20; i++ {
		mustSetDesired(t, model, grid.Grid{Cols: 40 + i, Rows: 20})
	}
	if model.Snapshot().Revision != 20 {
		t.Fatalf("local revision = %d", model.Snapshot().Revision)
	}
	if accepted, err := model.Observe(3, grid.Grid{Cols: 120, Rows: 40}); err != nil || !accepted {
		t.Fatalf("remote revision was confused with local intent: %v, %v", accepted, err)
	}
}

func TestValidationAndClosedErrors(t *testing.T) {
	model := newModel(t, grid.Config{Mode: grid.ModeController})
	for _, invalid := range []grid.Grid{{}, {Cols: 1}, {Rows: 1}, {Cols: -1, Rows: 2}, {Cols: 1025, Rows: 2}} {
		if _, err := model.SetDesired(invalid); err == nil {
			t.Fatalf("SetDesired(%+v) accepted invalid grid", invalid)
		}
	}
	if err := model.Flush(context.Background()); !errors.Is(err, grid.ErrNoDesiredGrid) {
		t.Fatalf("empty Flush error = %v", err)
	}
	model.Close()
	model.Close()
	if _, err := model.SetDesired(grid.Grid{Cols: 80, Rows: 24}); !errors.Is(err, grid.ErrClosed) {
		t.Fatalf("closed SetDesired error = %v", err)
	}
	if err := model.Claim(); !errors.Is(err, grid.ErrClosed) {
		t.Fatalf("closed Claim error = %v", err)
	}
	if !model.Snapshot().Closed {
		t.Fatal("snapshot does not report closed model")
	}
}
