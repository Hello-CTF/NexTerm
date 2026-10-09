package terminalgrid_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	grid "github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func newModel(t *testing.T, config grid.Config) *grid.Model {
	t.Helper()
	model, err := grid.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	return model
}

func mustSetDesired(t *testing.T, model *grid.Model, desired grid.Grid) uint64 {
	t.Helper()
	revision, err := model.SetDesired(desired)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

func assertCallCount(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls.Load() > want {
			t.Fatalf("transport calls = %d, want at most %d", calls.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != want {
		t.Fatalf("transport calls = %d, want %d", got, want)
	}
}

type controlledCall struct {
	grid   grid.Grid
	ctx    context.Context
	result chan error
}

func (c *controlledCall) finish(err error) {
	c.result <- err
}

type controlledTransport struct {
	calls chan *controlledCall
}

func newControlledTransport() *controlledTransport {
	return &controlledTransport{calls: make(chan *controlledCall, 128)}
}

func (f *controlledTransport) resize(ctx context.Context, desired grid.Grid) error {
	call := &controlledCall{grid: desired, ctx: ctx, result: make(chan error, 1)}
	f.calls <- call
	return <-call.result
}

func (f *controlledTransport) next(t *testing.T) *controlledCall {
	t.Helper()
	select {
	case call := <-f.calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for transport call")
		return nil
	}
}

func (f *controlledTransport) assertNoCall(t *testing.T) {
	t.Helper()
	select {
	case call := <-f.calls:
		t.Fatalf("unexpected transport call: %+v", call.grid)
	case <-time.After(30 * time.Millisecond):
	}
}
