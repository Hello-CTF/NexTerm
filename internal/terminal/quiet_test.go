package terminal

import (
	"sync"
	"testing"
	"time"
)

// The quiet facts are read from a fake clock: idle advances by advancing the
// clock, never by sleeping.

func quietTestTab(t *testing.T) (*Tab, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	tab := NewTab("t1", "s1", 80, 24, UTF8, WithClock(clock.now))
	t.Cleanup(tab.Close)
	return tab, clock
}

func TestQuietStateAdvancesOnOutput(t *testing.T) {
	tab, clock := quietTestTab(t)
	initial := tab.QuietState()
	if initial.Version != 0 || initial.Closed {
		t.Fatalf("initial quiet state = %+v, want version 0 open", initial)
	}
	clock.advance(100 * time.Millisecond)
	before := tab.QuietState()
	if before.IdleMS != 100 {
		t.Fatalf("idle before output = %d, want 100", before.IdleMS)
	}
	tab.Feed([]byte("output"))
	after := tab.QuietState()
	if after.Version != before.Version+1 {
		t.Fatalf("version after output = %d, want %d", after.Version, before.Version+1)
	}
	if after.IdleMS != 0 {
		t.Fatalf("idle after output = %d, want 0", after.IdleMS)
	}
	// Idle grows again purely by clock advance, with no further output.
	clock.advance(50 * time.Millisecond)
	if got := tab.QuietState(); got.IdleMS != 50 || got.Version != after.Version {
		t.Fatalf("quiet state after clock advance = %+v, want idle 50 version %d", got, after.Version)
	}
}

func TestQuietStateAdvancesOnResizeAndEncoding(t *testing.T) {
	tab, _ := quietTestTab(t)
	before := tab.QuietState()
	if err := tab.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	afterResize := tab.QuietState()
	if afterResize.Version != before.Version+1 {
		t.Fatalf("version after resize = %d, want %d", afterResize.Version, before.Version+1)
	}
	tab.SwitchEncoding(Latin1)
	if got := tab.QuietState(); got.Version != afterResize.Version+1 {
		t.Fatalf("version after encoding switch = %d, want %d", got.Version, afterResize.Version+1)
	}
}

func TestQuietStateModeChangeAdvancesVersion(t *testing.T) {
	tab, _ := quietTestTab(t)
	before := tab.QuietState()
	if before.Version != 0 {
		t.Fatalf("initial version = %d, want 0", before.Version)
	}
	// A mode-only escape sequence is still output activity: it advances the
	// version and flips the tracked mode in the same step.
	tab.Feed([]byte("\x1b[?2004h"))
	after := tab.QuietState()
	if after.Version != before.Version+1 {
		t.Fatalf("version after mode change = %d, want %d", after.Version, before.Version+1)
	}
	if !tab.State().BracketedPaste {
		t.Fatal("bracketed paste mode not tracked")
	}
}

func TestQuietStateCloseDistinguishable(t *testing.T) {
	tab, _ := quietTestTab(t)
	tab.Feed([]byte("output"))
	before := tab.QuietState()
	tab.Close()
	after := tab.QuietState()
	if !after.Closed {
		t.Fatal("closed tab not distinguished")
	}
	if after.Version != before.Version+1 {
		t.Fatalf("version after close = %d, want %d", after.Version, before.Version+1)
	}
	// A closed tab rejects further output: no version movement, no idle reset.
	tab.Feed([]byte("more"))
	if got := tab.QuietState(); got.Version != after.Version {
		t.Fatalf("closed tab advanced on feed: version = %d, want %d", got.Version, after.Version)
	}
}

// Race-shaped consistency check: concurrent feeds and quiet reads never
// produce a data race, and the version never moves backwards.
func TestQuietStateConcurrentFeedConsistent(t *testing.T) {
	tab, clock := quietTestTab(t)
	started := make(chan struct{})
	var wg sync.WaitGroup
	reads := make(chan TabQuietState, 200)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tab.Feed([]byte("x"))
		close(started)
		for i := 1; i < 200; i++ {
			tab.Feed([]byte("x"))
			clock.advance(time.Millisecond)
		}
	}()
	<-started // at least one feed landed, so the reader observes advances
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			reads <- tab.QuietState()
		}
	}()
	wg.Wait()
	close(reads)
	last := uint64(0)
	for state := range reads {
		if state.Version < last {
			t.Fatalf("version moved backwards: %d after %d", state.Version, last)
		}
		if state.IdleMS < 0 {
			t.Fatalf("negative idle: %+v", state)
		}
		last = state.Version
	}
	if last == 0 {
		t.Fatal("no version advance observed")
	}
}

// Regression for the r9 P1: output published while the version has not
// advanced yet must never be certified by a same-version quiet snapshot.
// The write side of the reviewer's interleave is reproduced exactly: a quiet
// write opens, the output timestamp is published inside it, and the write
// stays open across a concurrent read.
func TestQuietStateOpenWriteNeverCertifiesStaleState(t *testing.T) {
	tab, clock := quietTestTab(t)
	clock.advance(100 * time.Millisecond)
	tab.quietBegin()
	tab.lastOutput.Store(clock.now().UnixMilli())
	read := make(chan TabQuietState, 1)
	go func() { read <- tab.QuietState() }()
	select {
	case state := <-read:
		t.Fatalf("quiet read certified state during an open write: %+v", state)
	case <-time.After(50 * time.Millisecond):
	}
	tab.quietEnd()
	state := <-read
	if state.Version != 1 || state.IdleMS != 0 {
		t.Fatalf("state after the write closed = %+v, want version 1 idle 0", state)
	}
}

// The response handler runs after the quiet write closes: a blocked handler
// must never stall quiet readers.
func TestQuietStateResponseHandlerOutsideLock(t *testing.T) {
	tab, _ := quietTestTab(t)
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	tab.SetResponseHandler(func([]byte) {
		close(handlerStarted)
		<-handlerRelease
	})
	fed := make(chan struct{})
	go func() {
		tab.Feed([]byte("\x1b[6n"))
		close(fed)
	}()
	<-handlerStarted
	read := make(chan TabQuietState, 1)
	go func() { read <- tab.QuietState() }()
	select {
	case state := <-read:
		if state.Version != 1 {
			t.Fatalf("version = %d, want 1 (the handler must not hold the lock)", state.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quiet read blocked behind the response handler")
	}
	close(handlerRelease)
	<-fed
}
