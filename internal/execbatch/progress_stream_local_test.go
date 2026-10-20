//go:build !windows

package execbatch

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// These regressions run against the real local transport so the channel
// lifecycle (drain before close, stdin EOF) is exercised for real; the
// in-memory fake cannot model a Close that kills the process.

func localStreamTestTransport(t *testing.T) *local.Transport {
	t.Helper()
	transport := local.NewWithConfig(local.Config{})
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func pinSingleCPU(t *testing.T) {
	t.Helper()
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
}

func TestExecuteTargetStreamLocalLargeOutputDrainsBeforeClose(t *testing.T) {
	pinSingleCPU(t)
	transport := localStreamTestTransport(t)
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := Target{SessionID: "host-0", SessionName: "host-0", Transport: transport, Generation: transport.Generation()}
	command := "head -c 1048576 /dev/zero; printf 'LOCAL-STREAM-TAIL'"

	for attempt := 0; attempt < 2; attempt++ {
		tracker = NewProgressTracker("batch-1", []string{"host-0"})
		result, err := ExecuteTargetStream(context.Background(), tracker, target, command, TargetStreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusSuccess {
			t.Fatalf("attempt %d: result = %+v", attempt, result)
		}
		_, targets := tracker.Snapshot()
		stdout := targets[0].Output.Stdout
		if !strings.HasSuffix(stdout.Tail, "LOCAL-STREAM-TAIL") {
			t.Fatalf("attempt %d: tail lost the end marker: %q", attempt, stdout.Tail)
		}
		if !stdout.Truncated || stdout.TotalBytes != 1048576+len("LOCAL-STREAM-TAIL") {
			t.Fatalf("attempt %d: retention = %+v", attempt, stdout)
		}
	}
}

func TestExecuteTargetStreamLocalStdinEOFSucceeds(t *testing.T) {
	pinSingleCPU(t)
	transport := localStreamTestTransport(t)
	tracker := NewProgressTracker("batch-1", []string{"host-0"})
	target := Target{SessionID: "host-0", SessionName: "host-0", Transport: transport, Generation: transport.Generation()}

	result, err := ExecuteTargetStream(context.Background(), tracker, target, "cat; printf 'REVIEW-DONE'", TargetStreamOptions{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("EOF-driven command = %+v, want success", result)
	}
	_, targets := tracker.Snapshot()
	if !strings.Contains(targets[0].Output.Stdout.Head, "REVIEW-DONE") {
		t.Fatalf("stdout = %+v", targets[0].Output.Stdout)
	}
}

func TestExecuteTargetStreamLocalCancelKeepsSharedTransport(t *testing.T) {
	pinSingleCPU(t)
	transport := localStreamTestTransport(t)
	tracker := NewProgressTracker("batch-1", []string{"host-0", "host-1"})
	cancelTarget := Target{SessionID: "host-0", SessionName: "host-0", Transport: transport, Generation: transport.Generation()}
	nextTarget := Target{SessionID: "host-1", SessionName: "host-1", Transport: transport, Generation: transport.Generation()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		result, err := ExecuteTargetStream(ctx, tracker, cancelTarget, "sleep 30", TargetStreamOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	_, targets := tracker.Snapshot()
	for targets[0].Phase != ProgressPhaseRunning {
		time.Sleep(time.Millisecond)
		_, targets = tracker.Snapshot()
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != StatusCanceled {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled local stream did not finish")
	}

	result, err := ExecuteTargetStream(context.Background(), tracker, nextTarget, "printf 'still-alive'", TargetStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("shared transport lost after cancel: %+v", result)
	}
	_, targets = tracker.Snapshot()
	if !strings.Contains(targets[1].Output.Stdout.Head, "still-alive") {
		t.Fatalf("second exec output = %+v", targets[1].Output.Stdout)
	}
}
