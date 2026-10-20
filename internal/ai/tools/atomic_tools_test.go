package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestProductionWaitSnapshotKeepsTailAndIdleAtomic(t *testing.T) {
	fixture := newAtomicSessionFixture(t, true)
	fixture.clock.Add(100)
	var emitErr error
	fixture.setSnapshotHook(func() {
		emitErr = fixture.channel.emit([]byte("READY\r\n"))
	})
	adapter := SessionTerminal(fixture.manager)
	waitTerminal, ok := adapter.(WaitSnapshotTerminal)
	if !ok {
		t.Fatal("production terminal does not expose synchronized wait snapshots")
	}
	first, err := waitTerminal.WaitSnapshot(trustedContext(), fixture.tabID)
	if err != nil {
		t.Fatal(err)
	}
	if emitErr != nil {
		t.Fatal(emitErr)
	}
	if strings.Contains(strings.Join(first.Tail, "\n"), "READY") || first.IdleMS != 100 {
		t.Fatalf("mixed snapshot: tail=%q idle=%d", first.Tail, first.IdleMS)
	}
	deadline := time.Now().Add(time.Second)
	for {
		second, err := waitTerminal.WaitSnapshot(trustedContext(), fixture.tabID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(second.Tail, "\n"), "READY") {
			if second.IdleMS != 0 {
				t.Fatalf("new output kept stale idle: tail=%q idle=%d", second.Tail, second.IdleMS)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent output was not processed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWaitForQuietContinuousOutputTimeoutCancelAndZeroOutput(t *testing.T) {
	t.Run("zero output", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, true)
		fixture.clock.Add(100)
		result := fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "quiet", Name: "wait_for", Args: json.RawMessage(`{"quiet_ms":50,"timeout_ms":100}`)}, nil)
		if !result.OK || !strings.Contains(result.Text, "安静 100ms") {
			t.Fatalf("zero-output quiet result = %+v", result)
		}
		result = fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "never", Name: "wait_for", Args: json.RawMessage(`{"pattern":"never","quiet_ms":1,"timeout_ms":10}`)}, nil)
		if result.OK || !strings.Contains(result.Text, "等待超时") {
			t.Fatalf("zero-output nonmatch result = %+v", result)
		}
	})

	t.Run("continuous output", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, true)
		if err := fixture.channel.emit([]byte("READY\r\n")); err != nil {
			t.Fatal(err)
		}
		startClock := fixture.clock.Load()
		// Drive the fixture clock and the tab's output from the wait's own
		// polls instead of wall time: every snapshot advances the clock and
		// feeds the tab model directly (no pump round-trip), so the quiet
		// threshold can never be reached regardless of scheduling.
		var armHook func()
		armHook = func() {
			fixture.clock.Add(10)
			fixture.terminal().Feed([]byte("noise\r\n"))
			fixture.setSnapshotHook(armHook)
		}
		fixture.setSnapshotHook(armHook)
		result := fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "continuous", Name: "wait_for", Args: json.RawMessage(`{"pattern":"READY","quiet_ms":50,"timeout_ms":60}`)}, nil)
		fixture.setSnapshotHook(nil)
		timedOut := strings.Contains(result.Text, "等待超时") || strings.Contains(result.Text, context.DeadlineExceeded.Error())
		if result.OK || result.ExitCode != 1 || !timedOut || strings.Contains(result.Text, "模式已出现") {
			t.Fatalf("continuous output result = %+v", result)
		}
		if advanced := fixture.clock.Load() - startClock; advanced < 100 {
			t.Fatalf("test only advanced %dms, does not exceed quiet threshold", advanced)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, true)
		ctx, cancel := context.WithCancel(trustedContext())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		result := fixture.registry().Execute(ctx, "job", fixture.scope(), Call{ID: "cancel", Name: "wait_for", Args: json.RawMessage(`{"pattern":"never","quiet_ms":50,"timeout_ms":60000}`)}, nil)
		if result.OK || !strings.Contains(result.Text, context.Canceled.Error()) {
			t.Fatalf("cancel result = %+v", result)
		}
	})
}

func TestSessionPasteDECSETFlipsFailVisibly(t *testing.T) {
	for _, test := range []struct {
		name        string
		initiallyOn bool
		sequence    string
		generation  uint64
		firstWrite  string
	}{
		{name: "enabled then disabled", initiallyOn: true, sequence: "\x1b[?2004ldrained\r\n", generation: 1, firstWrite: "\x1b[200~prompt\x1b[201~"},
		{name: "disabled then enabled", initiallyOn: false, sequence: "\x1b[?2004hdrained\r\n", generation: 1, firstWrite: "prompt"},
		{name: "ABA mode flip", initiallyOn: true, sequence: "\x1b[?2004l\x1b[?2004hdrained\r\n", generation: 2, firstWrite: "\x1b[200~prompt\x1b[201~"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAtomicSessionFixture(t, false)
			if test.initiallyOn {
				if err := fixture.channel.emit([]byte("\x1b[?2004h")); err != nil {
					t.Fatal(err)
				}
				fixture.waitForMode(t, true)
			}
			_, initialGeneration := fixture.terminal().BracketedPasteMode()
			processed := make(chan struct{})
			var once sync.Once
			fixture.setWriteHook(func([]byte) {
				once.Do(func() {
					_ = fixture.channel.emit([]byte(test.sequence))
					deadline := time.Now().Add(500 * time.Millisecond)
					for {
						_, generation := fixture.terminal().BracketedPasteMode()
						if generation >= initialGeneration+test.generation {
							close(processed)
							return
						}
						if time.Now().After(deadline) {
							return
						}
						time.Sleep(time.Millisecond)
					}
				})
			})
			finished := make(chan Output, 1)
			go func() {
				finished <- fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "paste", Name: "send_keys", Args: json.RawMessage(`{"keys":"prompt","paste":true}`)}, nil)
			}()
			var result Output
			select {
			case result = <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("paste write deadlocked waiting for mode processing")
			}
			select {
			case <-processed:
			default:
				t.Fatal("output pump did not process DECSET while paste write was blocked")
			}
			if result.OK || !strings.Contains(result.Text, "paste mode changed") || !strings.Contains(result.Text, "partially written") || result.WriteConfirmed || result.SubmittedSeq != nil {
				t.Fatalf("mode flip result = %+v", result)
			}
			if writes := fixture.channel.successfulWrites(); len(writes) != 1 || string(writes[0]) != test.firstWrite {
				t.Fatalf("writes = %q, want initial encoding %q", writes, test.firstWrite)
			}
			tail, err := fixture.manager.TailLines(fixture.tabID, 30)
			if err != nil || !strings.Contains(strings.Join(tail, "\n"), "drained") {
				t.Fatalf("output was not drained during blocked write: %q, %v", tail, err)
			}
		})
	}
}

func TestSessionPasteStableModeSucceedsWhilePumpAndOtherTabProgress(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	if err := fixture.channel.emit([]byte("\x1b[?2004h")); err != nil {
		t.Fatal(err)
	}
	fixture.waitForMode(t, true)
	second, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: fixture.sessionID, ClientID: "second-client", ChannelID: "second-channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	fixture.setWriteHook(func([]byte) {
		startOnce.Do(func() {
			_ = fixture.channel.emit([]byte("drain\r\n"))
			close(started)
			<-release
		})
	})
	defer releaseOnce.Do(func() { close(release) })
	finished := make(chan Output, 1)
	go func() {
		finished <- fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "paste", Name: "send_keys", Args: json.RawMessage(`{"keys":"prompt","paste":true}`)}, nil)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("paste did not reach blocked write")
	}
	deadline := time.Now().Add(time.Second)
	for {
		dump, err := fixture.manager.RawDump(fixture.tabID, 1024)
		if err == nil && bytes.Contains(dump, []byte("drain")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("same-tab output pump stopped during paste write")
		}
		time.Sleep(time.Millisecond)
	}
	if err := fixture.manager.WriteInternal(context.Background(), second.ID, []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.InjectInternal(context.Background(), second.ID, []byte("feed")); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	var result Output
	select {
	case result = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stable paste did not finish")
	}
	if !result.OK || result.SubmittedSeq == nil || !result.WriteConfirmed || !strings.Contains(result.Notes, "not proof") {
		t.Fatalf("stable paste result = %+v", result)
	}
	if writes := fixture.transport.channelAt(1).successfulWrites(); len(writes) != 1 || string(writes[0]) != "second" {
		t.Fatalf("other tab writes = %q", writes)
	}
	dump, err := fixture.manager.RawDump(second.ID, 1024)
	if err != nil || !bytes.Contains(dump, []byte("feed")) {
		t.Fatalf("other tab feed = %q, %v", dump, err)
	}
}

func TestSessionPasteEnterFailureAndCancelDoNotRetry(t *testing.T) {
	t.Run("Enter failure", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, false)
		fixture.channel.mu.Lock()
		fixture.channel.failAt = 2
		fixture.channel.mu.Unlock()
		result := fixture.registry().Execute(trustedContext(), "job", fixture.scope(), Call{ID: "paste", Name: "send_keys", Args: json.RawMessage(`{"keys":"prompt","paste":true,"enter":true}`)}, nil)
		if result.OK || !strings.Contains(result.Text, "Enter") || result.WriteConfirmed || result.SubmittedSeq != nil {
			t.Fatalf("Enter failure result = %+v", result)
		}
		time.Sleep(200 * time.Millisecond)
		if got := fixture.channel.attemptCount(); got != 2 {
			t.Fatalf("write attempts = %d, want text and one failed Enter only", got)
		}
		if writes := fixture.channel.successfulWrites(); len(writes) != 1 || string(writes[0]) != "prompt" {
			t.Fatalf("successful writes = %q", writes)
		}
	})

	t.Run("cancel before Enter", func(t *testing.T) {
		fixture := newAtomicSessionFixture(t, false)
		ctx, cancel := context.WithCancel(trustedContext())
		written := make(chan struct{})
		var once sync.Once
		fixture.setWriteHook(func([]byte) {
			once.Do(func() { close(written) })
		})
		finished := make(chan Output, 1)
		go func() {
			finished <- fixture.registry().Execute(ctx, "job", fixture.scope(), Call{ID: "paste", Name: "send_keys", Args: json.RawMessage(`{"keys":"prompt","paste":true,"enter":true}`)}, nil)
		}()
		<-written
		cancel()
		result := <-finished
		if result.OK || !strings.Contains(result.Text, "Enter") || result.WriteConfirmed || result.SubmittedSeq != nil {
			t.Fatalf("cancel result = %+v", result)
		}
		time.Sleep(200 * time.Millisecond)
		if got := fixture.channel.attemptCount(); got != 1 {
			t.Fatalf("cancel generated %d writes, want no Enter retry", got)
		}
	})
}
