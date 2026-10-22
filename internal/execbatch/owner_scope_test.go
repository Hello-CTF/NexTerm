package execbatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func blockingTarget(id string, started chan<- context.Context) Target {
	return targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- ctx
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	}))
}

func TestExecuteAdmitsSameBatchIDForDifferentOwners(t *testing.T) {
	started := map[string]chan context.Context{"a": make(chan context.Context, 1), "b": make(chan context.Context, 1)}
	resolver := &fakeResolver{targets: map[string]Target{
		"a": blockingTarget("a", started["a"]),
		"b": blockingTarget("b", started["b"]),
	}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	doneA := make(chan outcome, 1)
	doneB := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "shared-id", SessionIDs: []string{"a"}, Command: "wait"})
		doneA <- outcome{batch: batch, err: err}
	}()
	go func() {
		batch, err := service.Execute(t.Context(), "owner-b", Request{BatchID: "shared-id", SessionIDs: []string{"b"}, Command: "wait"})
		doneB <- outcome{batch: batch, err: err}
	}()
	for id, ch := range started {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("owner %s batch did not start; the shared batchID must not conflict across owners", id)
		}
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "shared-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cancel(t.Context(), "owner-b", "shared-id"); err != nil {
		t.Fatal(err)
	}
	for owner, done := range map[string]chan outcome{"owner-a": doneA, "owner-b": doneB} {
		select {
		case result := <-done:
			if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
				t.Fatalf("%s outcome = %+v, %v", owner, result.batch, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s batch did not finish", owner)
		}
	}
}

func TestExecuteConflictsOnlyWithinTheOwnerNamespace(t *testing.T) {
	started := make(chan context.Context, 1)
	resolver := &fakeResolver{targets: map[string]Target{"a": blockingTarget("a", started)}}
	service := New(Config{Resolver: resolver})
	done := make(chan Batch, 1)
	go func() {
		batch, _ := service.Execute(t.Context(), "owner-a", Request{BatchID: "dup", SessionIDs: []string{"a"}, Command: "wait"})
		done <- batch
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch did not start")
	}
	if _, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "dup", SessionIDs: []string{"a"}, Command: "wait"}); !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("same-owner duplicate error = %v, want ErrBatchConflict", err)
	}
	other := make(chan Batch, 1)
	go func() {
		batch, _ := service.Execute(t.Context(), "owner-b", Request{BatchID: "dup", SessionIDs: []string{"a"}, Command: "wait"})
		other <- batch
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-started:
			if _, err := service.Cancel(t.Context(), "owner-b", "dup"); err != nil {
				t.Fatal(err)
			}
		case batch := <-other:
			if len(batch.Results) != 1 || batch.Results[0].Status != StatusCanceled {
				t.Fatalf("owner-b batch = %+v", batch)
			}
			if _, err := service.Cancel(t.Context(), "owner-a", "dup"); err != nil {
				t.Fatal(err)
			}
			<-done
			return
		case <-deadline:
			t.Fatal("owner-b batch with a reused batchID did not finish")
		}
	}
}

func TestCancelMatchesOnlyTheCallersNamespace(t *testing.T) {
	started := make(chan context.Context, 1)
	var execCalls atomic.Int32
	resolver := &fakeResolver{targets: map[string]Target{
		"a": targetFor("a", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			execCalls.Add(1)
			started <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		})),
	}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	doneA := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "shared-cancel", SessionIDs: []string{"a"}, Command: "wait"})
		doneA <- outcome{batch: batch, err: err}
	}()
	execCtx := <-started

	cancelB, err := service.Cancel(t.Context(), "owner-b", "shared-cancel")
	if err != nil || !cancelB.Canceled {
		t.Fatalf("cross-owner cancel must be accepted in the caller's own namespace: %+v, %v", cancelB, err)
	}
	if execCtx.Err() != nil {
		t.Fatal("owner B cancel affected owner A's active batch")
	}

	batchB, err := service.Execute(t.Context(), "owner-b", Request{BatchID: "shared-cancel", SessionIDs: []string{"a"}, Command: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batchB.Results) != 1 || batchB.Results[0].Status != StatusCanceled {
		t.Fatalf("owner B pending cancel did not apply to owner B's own batch: %+v", batchB)
	}
	if execCalls.Load() != 1 {
		t.Fatalf("owner B early-canceled batch executed a command: calls = %d", execCalls.Load())
	}
	if execCtx.Err() != nil {
		t.Fatal("owner B pending cancel leaked into owner A's batch")
	}

	if _, err := service.Cancel(t.Context(), "owner-a", "shared-cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-doneA:
		if result.err != nil || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("owner A outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner A batch did not finish")
	}
}

func TestProbeHealthAdmitsSameBatchIDForDifferentOwners(t *testing.T) {
	started := map[string]chan context.Context{"a": make(chan context.Context, 1), "b": make(chan context.Context, 1)}
	resolver := &fakeResolver{targets: map[string]Target{
		"a": blockingTarget("a", started["a"]),
		"b": blockingTarget("b", started["b"]),
	}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch ExecBatchHealth
		err   error
	}
	doneA := make(chan outcome, 1)
	doneB := make(chan outcome, 1)
	go func() {
		batch, err := service.ProbeHealth(t.Context(), "owner-a", ExecBatchHealthRequest{
			BatchID: "health-shared", Targets: []ExecBatchHealthTarget{{AssetID: "asset-a", SessionID: "a"}},
		})
		doneA <- outcome{batch: batch, err: err}
	}()
	go func() {
		batch, err := service.ProbeHealth(t.Context(), "owner-b", ExecBatchHealthRequest{
			BatchID: "health-shared", Targets: []ExecBatchHealthTarget{{AssetID: "asset-b", SessionID: "b"}},
		})
		doneB <- outcome{batch: batch, err: err}
	}()
	for id, ch := range started {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("owner %s health probe did not start; the shared batchID must not conflict across owners", id)
		}
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "health-shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Cancel(t.Context(), "owner-b", "health-shared"); err != nil {
		t.Fatal(err)
	}
	for owner, done := range map[string]chan outcome{"owner-a": doneA, "owner-b": doneB} {
		select {
		case result := <-done:
			if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
				t.Fatalf("%s health outcome = %+v, %v", owner, result.batch, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s health probe did not finish", owner)
		}
	}
}

func TestProbeHealthConflictsWithSameOwnerActiveBatchOnly(t *testing.T) {
	started := make(chan context.Context, 1)
	resolver := &fakeResolver{targets: map[string]Target{"a": blockingTarget("a", started)}}
	service := New(Config{Resolver: resolver})
	done := make(chan Batch, 1)
	go func() {
		batch, _ := service.Execute(t.Context(), "owner-a", Request{BatchID: "mixed", SessionIDs: []string{"a"}, Command: "wait"})
		done <- batch
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("execute did not start")
	}
	if _, err := service.ProbeHealth(t.Context(), "owner-a", ExecBatchHealthRequest{
		BatchID: "mixed", Targets: []ExecBatchHealthTarget{{AssetID: "asset-a", SessionID: "a"}},
	}); !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("same-owner probe error = %v, want ErrBatchConflict", err)
	}
	probeDone := make(chan error, 1)
	go func() {
		_, err := service.ProbeHealth(t.Context(), "owner-b", ExecBatchHealthRequest{
			BatchID: "mixed", Targets: []ExecBatchHealthTarget{{AssetID: "asset-b", SessionID: "a"}},
		})
		probeDone <- err
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-started:
			if _, err := service.Cancel(t.Context(), "owner-b", "mixed"); err != nil {
				t.Fatal(err)
			}
		case err := <-probeDone:
			if err != nil {
				t.Fatalf("owner-b probe with the same batchID failed: %v", err)
			}
			if _, err := service.Cancel(t.Context(), "owner-a", "mixed"); err != nil {
				t.Fatal(err)
			}
			<-done
			return
		case <-deadline:
			t.Fatal("owner-b probe with a reused batchID did not finish")
		}
	}
}
