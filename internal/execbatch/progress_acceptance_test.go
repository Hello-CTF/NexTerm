package execbatch

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// TestExecBatchTenHostSimulatedAcceptance is the deterministic R-10
// acceptance scenario over the progress/output/rerun primitives. It drives
// 10 simulated hosts in-process with interleaved stdout/stderr, mixed
// outcomes, and a cancellation; no real SSH sessions are involved. Wiring
// these primitives to the service and verifying 10 live SSH hosts remains
// outstanding for the R-10 integration phase.
func TestExecBatchTenHostSimulatedAcceptance(t *testing.T) {
	const hosts = 10
	const chunksPerStream = 32
	const chunkBytes = 8 << 10

	tracker := NewProgressTracker("batch-acceptance", []string{
		"session-00", "session-01", "session-02", "session-03", "session-04",
		"session-05", "session-06", "session-07", "session-08", "session-09",
	})
	base := time.Unix(1_700_000_000, 0)

	outcomes := []Status{
		StatusSuccess, StatusSuccess, StatusExitError, StatusSuccess, StatusSuccess,
		StatusTransportError, StatusSuccess, StatusTimeout, StatusSuccess, StatusCanceled,
	}

	hostStream := func(host int, stream string) []byte {
		total := chunksPerStream * chunkBytes
		header := fmt.Sprintf("host-%02d-%s\n", host, stream)
		ender := fmt.Sprintf("host-%02d-%s-END", host, stream)
		body := make([]byte, 0, total)
		body = append(body, header...)
		line := "line 中é 0123456789\n"
		for len(body)+len(line)+len(ender) <= total {
			body = append(body, line...)
		}
		for len(body)+len(ender) < total {
			body = append(body, 'x')
		}
		return append(body, ender...)
	}

	var workers sync.WaitGroup
	for host := range hosts {
		workers.Add(1)
		go func(host int) {
			defer workers.Done()
			sessionID := fmt.Sprintf("session-%02d", host)
			if err := tracker.Start(sessionID, base); err != nil {
				t.Error(err)
				return
			}
			stdout := hostStream(host, "out")
			stderr := hostStream(host, "err")
			chunks, size := chunksPerStream, chunkBytes
			if outcomes[host] == StatusCanceled {
				chunks, size = 2, 1<<10
			}
			for chunk := 0; chunk < chunks; chunk++ {
				begin := chunk * size
				if err := tracker.WriteStdout(sessionID, stdout[begin:begin+size]); err != nil {
					t.Error(err)
					return
				}
				if err := tracker.WriteStderr(sessionID, stderr[begin:begin+size]); err != nil {
					t.Error(err)
					return
				}
			}
			result := progressTestResult(outcomes[host])
			result.DurationMS = int64(host+1) * 15
			if err := tracker.Finish(sessionID, result, base.Add(time.Duration(host+1)*time.Second)); err != nil {
				t.Error(err)
			}
		}(host)
	}
	pollers := sync.WaitGroup{}
	for range 3 {
		pollers.Add(1)
		go func() {
			defer pollers.Done()
			for {
				batch, _ := tracker.Snapshot()
				if batch.Phase == ProgressPhaseFinished {
					return
				}
				_ = tracker.Events(0)
			}
		}()
	}
	workers.Wait()
	pollers.Wait()

	batch, targets := tracker.Snapshot()
	if batch.Phase != ProgressPhaseFinished || batch.Total != hosts ||
		batch.Succeeded != 6 || batch.Failed != 3 || batch.Canceled != 1 ||
		batch.Pending != 0 || batch.Running != 0 || batch.FinishedAt == 0 || batch.DroppedEvents != 0 {
		t.Fatalf("final rollup = %+v", batch)
	}

	results := make([]Result, hosts)
	for host, target := range targets {
		if target.Phase != ProgressPhaseFinished || target.Status != outcomes[host] {
			t.Fatalf("target %s = %+v", target.SessionID, target)
		}
		if target.StartedAt != base.UnixMilli() || target.FinishedAt == 0 || target.DurationMS != int64(host+1)*15 {
			t.Fatalf("target %s timing = %+v", target.SessionID, target)
		}
		stdout, stderr := target.Output.Stdout, target.Output.Stderr
		if !utf8.ValidString(stdout.Head) || !utf8.ValidString(stdout.Tail) ||
			!utf8.ValidString(stderr.Head) || !utf8.ValidString(stderr.Tail) {
			t.Fatalf("target %s retained invalid UTF-8: %+v", target.SessionID, target.Output)
		}
		if !strings.HasPrefix(stdout.Head, fmt.Sprintf("host-%02d-out\n", host)) ||
			!strings.HasPrefix(stderr.Head, fmt.Sprintf("host-%02d-err\n", host)) {
			t.Fatalf("target %s lost stream head: %+v", target.SessionID, target.Output)
		}
		results[host] = Result{SessionID: target.SessionID, Status: target.Status}
		if outcomes[host] == StatusCanceled {
			if stdout.Truncated || stdout.TotalBytes != 2*(1<<10) {
				t.Fatalf("canceled target %s output = %+v", target.SessionID, stdout)
			}
			continue
		}
		if want := len(hostStream(host, "out")); stdout.TotalBytes != want {
			t.Fatalf("target %s total bytes = %d, want %d", target.SessionID, stdout.TotalBytes, want)
		}
		if !strings.HasSuffix(stdout.Tail, fmt.Sprintf("host-%02d-out-END", host)) ||
			!strings.HasSuffix(stderr.Tail, fmt.Sprintf("host-%02d-err-END", host)) {
			t.Fatalf("target %s lost stream tail: %+v", target.SessionID, target.Output)
		}
		if !stdout.Truncated || stdout.DroppedBytes != stdout.TotalBytes-len(stdout.Head)-len(stdout.Tail) {
			t.Fatalf("target %s retention accounting = %+v", target.SessionID, stdout)
		}
	}

	events := tracker.Events(0)
	wantEvents := (hosts-1)*(2*chunksPerStream+2) + (2*2 + 2) + 1
	if int64(len(events)) != int64(wantEvents) {
		t.Fatalf("events = %d, want %d", len(events), wantEvents)
	}
	perHostSeen := make(map[string][]ProgressEventKind, hosts)
	for index, event := range events {
		if event.Seq != int64(index+1) {
			t.Fatalf("event %d seq = %d", index, event.Seq)
		}
		if event.Kind == ProgressEventBatchFinished {
			if index != len(events)-1 || event.Target != nil {
				t.Fatalf("batch_finished at %d of %d", index, len(events))
			}
			continue
		}
		perHostSeen[event.Target.SessionID] = append(perHostSeen[event.Target.SessionID], event.Kind)
	}
	for host := range hosts {
		sessionID := fmt.Sprintf("session-%02d", host)
		kinds := perHostSeen[sessionID]
		if len(kinds) == 0 || kinds[0] != ProgressEventTargetStarted || kinds[len(kinds)-1] != ProgressEventTargetFinished {
			t.Fatalf("host %s event order = %+v", sessionID, kinds)
		}
	}

	selected := SelectRerunTargets(results)
	if strings.Join(selected, ",") != "session-02,session-05,session-07" {
		t.Fatalf("rerun selection = %+v", selected)
	}
	var successes []Result
	for _, result := range results {
		if result.Status == StatusSuccess {
			successes = append(successes, result)
		}
	}
	if rerun := SelectRerunTargets(successes); len(rerun) != 0 {
		t.Fatalf("empty-failure rerun = %+v", rerun)
	}
}
