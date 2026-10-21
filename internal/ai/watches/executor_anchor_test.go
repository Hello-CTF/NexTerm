package watches

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestExecutorWatchAfterObservedChunkUsesTrueLeftContext(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		_ = executor.Stop(ctx)
	})
	source := &scriptSource{gate: gate, gateAfter: 1, steps: []scriptStep{
		{data: []byte("not yet\n")},
		{data: []byte("xxready\n")},
	}}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)

	executor.mu.Lock()
	runner := executor.targets["target-1"]
	executor.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runner.observeMu.Lock()
		pos := runner.pos
		runner.observeMu.Unlock()
		if pos == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner position = %d; want 8 with later output gated", pos)
		}
		time.Sleep(time.Millisecond)
	}

	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if watch.AnchorSeq != 8 {
		t.Fatalf("watch anchor = %d; want the observed position 8", watch.AnchorSeq)
	}
	row, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(row.ContextBlob) != "not yet\n" {
		t.Fatalf("watch left context = %q; want the true bytes before anchor 8", row.ContextBlob)
	}

	release()
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	data := waitDelivery(t, sink)
	if data.Channel != "tool" || !data.Untrusted || data.Event.MatchedFrom != 10 || data.Event.MatchedTo != 15 {
		t.Fatalf("delivery after observed-chunk create = %+v; want untrusted tool data matching 10..15", data)
	}
}
