package execbatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func progressTestResult(status Status) Result {
	result := Result{Status: status}
	switch status {
	case StatusExitError:
		code := 1
		result.ExitCode = &code
	case StatusTransportError:
		result.Error = "transport exploded"
	case StatusTimeout:
		result.Error = context.DeadlineExceeded.Error()
	case StatusCanceled:
		result.Error = context.Canceled.Error()
	}
	return result
}

func TestProgressTrackerLifecycleAndEvents(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"a", "b", "c"})
	base := time.Unix(1_700_000_000, 0)

	batch, targets := tracker.Snapshot()
	if batch.Phase != ProgressPhasePending || batch.Total != 3 || batch.Pending != 3 || len(targets) != 3 {
		t.Fatalf("initial snapshot = %+v, %d targets", batch, len(targets))
	}
	if targets[0].SessionID != "a" || targets[1].Position != 1 || targets[2].SessionID != "c" {
		t.Fatalf("initial targets = %+v", targets)
	}

	if err := tracker.Start("a", base); err != nil {
		t.Fatal(err)
	}
	if err := tracker.WriteStdout("a", []byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if err := tracker.WriteStderr("a", []byte("warn")); err != nil {
		t.Fatal(err)
	}
	if err := tracker.WriteStdout("a", []byte("world")); err != nil {
		t.Fatal(err)
	}

	batch, targets = tracker.Snapshot()
	if batch.Phase != ProgressPhaseRunning || batch.Running != 1 || batch.Pending != 2 {
		t.Fatalf("running snapshot = %+v", batch)
	}
	if targets[0].Output.Stdout.Head != "hello world" || targets[0].Output.Stderr.Head != "warn" {
		t.Fatalf("running target output = %+v", targets[0].Output)
	}
	if targets[0].StartedAt != base.UnixMilli() {
		t.Fatalf("startedAt = %d", targets[0].StartedAt)
	}

	if err := tracker.Finish("a", progressTestResult(StatusSuccess), base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Finish("b", progressTestResult(StatusExitError), base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Start("b", base); err == nil {
		t.Fatal("start after finish succeeded")
	}
	batch, targets = tracker.Snapshot()
	if batch.Phase != ProgressPhaseRunning || batch.Succeeded != 1 || batch.Failed != 1 || batch.Pending != 1 {
		t.Fatalf("partial snapshot = %+v", batch)
	}
	if targets[1].Phase != ProgressPhaseFinished || targets[1].Status != StatusExitError || *targets[1].ExitCode != 1 {
		t.Fatalf("finished target = %+v", targets[1])
	}

	if err := tracker.Finish("c", progressTestResult(StatusCanceled), base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	batch, _ = tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Canceled != 1 || batch.FinishedAt != base.Add(2*time.Second).UnixMilli() {
		t.Fatalf("final snapshot = %+v", batch)
	}

	events := tracker.Events(0)
	wantKinds := []ProgressEventKind{
		ProgressEventTargetStarted,
		ProgressEventTargetOutput, ProgressEventTargetOutput, ProgressEventTargetOutput,
		ProgressEventTargetFinished,
		ProgressEventTargetFinished,
		ProgressEventTargetFinished,
		ProgressEventBatchFinished,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("events = %d, want %d: %+v", len(events), len(wantKinds), events)
	}
	for index, event := range events {
		if event.Seq != int64(index+1) || event.BatchID != "batch-1" {
			t.Fatalf("event %d = %+v", index, event)
		}
		if event.Kind != wantKinds[index] {
			t.Fatalf("event %d kind = %s, want %s", index, event.Kind, wantKinds[index])
		}
	}
	if events[len(events)-1].Target != nil {
		t.Fatal("batch event carries a target")
	}
	if events[0].Target == nil || events[0].Target.SessionID != "a" {
		t.Fatalf("start event = %+v", events[0])
	}
	if events[3].Target.Output.Stdout.Head != "hello world" {
		t.Fatalf("output event = %+v", events[3])
	}
	if tail := tracker.Events(int64(len(events) - 2)); len(tail) != 2 || tail[0].Seq != events[len(events)-2].Seq {
		t.Fatalf("tail events = %+v", tail)
	}
}

func TestProgressTrackerValidation(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"a"})
	now := time.Unix(1_700_000_000, 0)

	if err := tracker.Start("missing", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("start unknown = %v", err)
	}
	if err := tracker.WriteStdout("missing", []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("write unknown = %v", err)
	}
	if err := tracker.Finish("missing", progressTestResult(StatusSuccess), now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finish unknown = %v", err)
	}
	if err := tracker.WriteStdout("a", []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("write before start = %v", err)
	}
	if err := tracker.Start("a", now); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Start("a", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double start = %v", err)
	}
	if err := tracker.Finish("a", progressTestResult(StatusSuccess), now); err != nil {
		t.Fatal(err)
	}
	if err := tracker.WriteStdout("a", []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("write after finish = %v", err)
	}
	if err := tracker.Finish("a", progressTestResult(StatusSuccess), now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double finish = %v", err)
	}
}

func TestProgressTrackerDuplicateTargetsCollapse(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"a", "b", "a"})
	batch, targets := tracker.Snapshot()
	if batch.Total != 2 || len(targets) != 2 || targets[0].SessionID != "a" || targets[1].SessionID != "b" {
		t.Fatalf("snapshot = %+v, %+v", batch, targets)
	}
}

func TestProgressTrackerRollupCounts(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"s1", "s2", "s3", "s4", "s5", "s6"})
	now := time.Unix(1_700_000_000, 0)
	statuses := []Status{StatusSuccess, StatusExitError, StatusTransportError, StatusTimeout, StatusCanceled, StatusSuccess}
	for index, sessionID := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		if err := tracker.Start(sessionID, now); err != nil {
			t.Fatal(err)
		}
		if err := tracker.Finish(sessionID, progressTestResult(statuses[index]), now); err != nil {
			t.Fatal(err)
		}
	}
	batch, _ := tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Succeeded != 2 || batch.Failed != 3 || batch.Canceled != 1 || batch.Pending != 0 || batch.Running != 0 {
		t.Fatalf("rollup = %+v", batch)
	}
}

func TestProgressTrackerEventLogBound(t *testing.T) {
	tracker := NewProgressTracker("batch-1", []string{"a"})
	now := time.Unix(1_700_000_000, 0)
	if err := tracker.Start("a", now); err != nil {
		t.Fatal(err)
	}
	for range maxProgressEvents {
		if err := tracker.WriteStdout("a", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	batch, _ := tracker.Snapshot()
	if batch.DroppedEvents == 0 {
		t.Fatal("event log bound did not drop anything")
	}
	events := tracker.Events(0)
	if len(events) != maxProgressEvents {
		t.Fatalf("retained events = %d, want %d", len(events), maxProgressEvents)
	}
	if events[0].Seq != 2 || events[len(events)-1].Seq != int64(maxProgressEvents+1) {
		t.Fatalf("retained seq range = %d..%d", events[0].Seq, events[len(events)-1].Seq)
	}
	if err := tracker.Finish("a", progressTestResult(StatusSuccess), now); err != nil {
		t.Fatal(err)
	}
	batch, _ = tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished {
		t.Fatalf("finish after dropped events = %+v", batch)
	}
	if last := tracker.Events(0); len(last) == 0 || last[len(last)-1].Kind != ProgressEventBatchFinished {
		t.Fatalf("missing batch_finished after drop: %+v", last)
	}
}

func TestProgressTrackerConcurrentHosts(t *testing.T) {
	const hosts = 8
	const writes = 200
	tracker := NewProgressTracker("batch-1", []string{"h0", "h1", "h2", "h3", "h4", "h5", "h6", "h7"})
	now := time.Unix(1_700_000_000, 0)

	var workers sync.WaitGroup
	for host := 0; host < hosts; host++ {
		workers.Add(1)
		go func(host int) {
			defer workers.Done()
			sessionID := fmt.Sprintf("h%d", host)
			if err := tracker.Start(sessionID, now); err != nil {
				t.Error(err)
				return
			}
			for write := 0; write < writes; write++ {
				if err := tracker.WriteStdout(sessionID, []byte("0123456789")); err != nil {
					t.Error(err)
					return
				}
			}
			if err := tracker.Finish(sessionID, progressTestResult(StatusSuccess), now); err != nil {
				t.Error(err)
			}
		}(host)
	}
	readers := sync.WaitGroup{}
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				batch, targets := tracker.Snapshot()
				if batch.Phase == ProgressPhaseFinished {
					_ = targets
					return
				}
				_ = tracker.Events(0)
			}
		}()
	}
	workers.Wait()
	readers.Wait()

	batch, targets := tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Succeeded != hosts {
		t.Fatalf("final batch = %+v", batch)
	}
	for _, target := range targets {
		if target.Output.Stdout.TotalBytes != writes*10 || target.Output.Stdout.Truncated || target.Output.Stdout.Head == "" {
			t.Fatalf("target %s output = %+v", target.SessionID, target.Output.Stdout)
		}
	}
	events := tracker.Events(0)
	if int64(len(events)) != int64(hosts)*(writes+2)+1 {
		t.Fatalf("events = %d", len(events))
	}
}
