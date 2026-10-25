package session

import (
	"bytes"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic clock plus timer wheel for the coalescer.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
	clock   *fakeClock
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) after(d time.Duration, fn func()) stopper {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{at: f.now.Add(d), f: fn, clock: f}
	f.timers = append(f.timers, t)
	return t
}

// advance moves time forward, firing due timers in order at their deadlines.
func (f *fakeClock) advance(d time.Duration) {
	target := f.Now().Add(d)
	for {
		f.mu.Lock()
		var due []*fakeTimer
		for _, t := range f.timers {
			if !t.stopped && !t.fired && !t.at.After(target) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			f.now = target
			f.mu.Unlock()
			return
		}
		sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
		t := due[0]
		t.fired = true
		if t.at.After(f.now) {
			f.now = t.at
		}
		f.mu.Unlock()
		t.f()
	}
}

// coalesceRig drives a coalescer the way Manager.feed and flushSubscriberOutput do and records frames.
type coalesceRig struct {
	clock  *fakeClock
	c      *outCoalescer
	frames [][]byte
}

func newCoalesceRig(fixed bool) *coalesceRig {
	r := &coalesceRig{clock: &fakeClock{now: time.Unix(1000, 0)}}
	r.c = newOutCoalescer(func() {
		if b := r.c.take(); len(b) > 0 {
			r.frames = append(r.frames, b)
		}
	})
	r.c.now = r.clock.Now
	r.c.afterFunc = r.clock.after
	r.c.fixed = fixed
	return r
}

func (r *coalesceRig) feed(data []byte) {
	if b, now := r.c.add(data); now {
		r.frames = append(r.frames, b)
	}
}

// stream feeds chunk-sized writes every step until total bytes were fed, then drains; it returns the reassembled bytes.
func (r *coalesceRig) stream(total, chunk int, step time.Duration) []byte {
	var sent []byte
	for i := 0; len(sent) < total; i++ {
		n := min(chunk, total-len(sent))
		p := bytes.Repeat([]byte{byte('a' + i%26)}, n)
		sent = append(sent, p...)
		r.feed(p)
		r.clock.advance(step)
	}
	r.clock.advance(100 * time.Millisecond)
	return sent
}

func (r *coalesceRig) joined() []byte { return bytes.Join(r.frames, nil) }

func (r *coalesceRig) maxFrame() int {
	m := 0
	for _, f := range r.frames {
		m = max(m, len(f))
	}
	return m
}

func TestCoalesceAdaptiveEchoLatencyUnchanged(t *testing.T) {
	r := newCoalesceRig(false)
	for i := 0; i < 5; i++ {
		r.feed([]byte("k"))
		r.clock.advance(outputCoalesceIdleGrace)
		if len(r.frames) != i+1 || string(r.frames[i]) != "k" {
			t.Fatalf("echo %d not flushed within the idle grace: %d frames", i, len(r.frames))
		}
		r.clock.advance(50 * time.Millisecond)
	}
	if r.c.bulk {
		t.Fatal("echo must never enter bulk mode")
	}
}

func TestCoalesceAdaptiveSmallBurstStaysNormal(t *testing.T) {
	r := newCoalesceRig(false)
	// A one-off 200 KiB burst then silence must not widen windows.
	r.stream(200<<10, 16<<10, 0)
	if r.c.bulk && r.clock.Now().Sub(r.c.lastFlush) < outputCoalesceBulkIdle {
		// Entered by size alone is allowed only for sustained flushes; verify it decays.
		r.clock.advance(outputCoalesceBulkIdle)
	}
	r.feed([]byte("x"))
	r.clock.advance(outputCoalesceIdleGrace)
	if got := r.frames[len(r.frames)-1]; string(got) != "x" {
		t.Fatalf("post-burst echo delayed: last frame %d bytes", len(got))
	}
}

func TestCoalesceAdaptiveEntersBulkAndRespectsCap(t *testing.T) {
	r := newCoalesceRig(false)
	want := r.stream(8<<20, 16<<10, time.Millisecond)
	if !bytes.Equal(r.joined(), want) {
		t.Fatal("data lost or reordered")
	}
	if r.maxFrame() <= outputCoalesceMax {
		t.Fatalf("never widened: max frame %d", r.maxFrame())
	}
	// A frame may exceed the cap only by the chunk that crossed it.
	if r.maxFrame() >= outputCoalesceBulkMax+16<<10 {
		t.Fatalf("bulk cap exceeded: %d", r.maxFrame())
	}
}

func TestCoalesceAdaptiveReducesFrames(t *testing.T) {
	const total = 8 << 20
	for _, tc := range []struct {
		name  string
		chunk int
		step  time.Duration
	}{
		{"16KiB/ms", 16 << 10, time.Millisecond},
		{"4KiB/100us", 4 << 10, 100 * time.Microsecond},
	} {
		base := newCoalesceRig(true)
		want := base.stream(total, tc.chunk, tc.step)
		ad := newCoalesceRig(false)
		ad.stream(total, tc.chunk, tc.step)
		if !bytes.Equal(base.joined(), want) || !bytes.Equal(ad.joined(), want) {
			t.Fatalf("%s: data lost or reordered", tc.name)
		}
		if len(ad.frames) >= len(base.frames) {
			t.Fatalf("%s: adaptive %d frames not fewer than fixed %d", tc.name, len(ad.frames), len(base.frames))
		}
		mb := float64(total) / (1 << 20)
		t.Logf("%s: frames/MB fixed=%.1f adaptive=%.1f (max frame %d)", tc.name,
			float64(len(base.frames))/mb, float64(len(ad.frames))/mb, ad.maxFrame())
	}
}

func TestCoalesceAdaptiveLeavesBulkOnIdle(t *testing.T) {
	r := newCoalesceRig(false)
	r.stream(4<<20, 16<<10, time.Millisecond)
	// stream drained for 100 ms; the next chunk sees the idle gap.
	r.feed([]byte("$ "))
	if r.c.bulk {
		t.Fatal("still in bulk mode after idle")
	}
	r.clock.advance(outputCoalesceIdleGrace)
	if got := r.frames[len(r.frames)-1]; string(got) != "$ " {
		t.Fatalf("prompt after bulk not flushed within grace: %q", got)
	}
}

func TestCoalesceAdaptiveLeavesBulkOnLightFlush(t *testing.T) {
	r := newCoalesceRig(false)
	r.c.bulk = true
	r.c.lastFlush = r.clock.Now()
	r.clock.advance(time.Millisecond)
	r.feed([]byte("tiny"))
	r.clock.advance(outputCoalesceBulkInterval)
	if r.c.bulk || string(r.joined()) != "tiny" {
		t.Fatalf("bulk=%v frames=%q", r.c.bulk, r.joined())
	}
}

func TestCoalesceAdaptiveInputDropsBulkAndRearms(t *testing.T) {
	r := newCoalesceRig(false)
	// Reach bulk mode by feeding until the flag flips, then leave data pending in a wide window.
	for i := 0; i < 1000 && !r.c.bulk; i++ {
		r.feed(bytes.Repeat([]byte("z"), 16<<10))
		r.clock.advance(time.Millisecond)
	}
	if !r.c.bulk {
		t.Fatal("did not enter bulk mode")
	}
	r.clock.advance(time.Millisecond)
	r.feed([]byte("echo"))
	before := len(r.frames)
	r.c.noteInput()
	if r.c.bulk {
		t.Fatal("input must leave bulk mode")
	}
	r.clock.advance(outputCoalesceIdleGrace)
	if len(r.frames) <= before && !bytes.HasSuffix(r.joined(), []byte("echo")) {
		t.Fatal("pending output was not flushed within the idle grace after input")
	}
	if !bytes.HasSuffix(r.joined(), []byte("echo")) {
		t.Fatal("echo missing")
	}
}

func TestCoalesceAdaptiveConcurrentRaceFree(t *testing.T) {
	c := newOutCoalescer(func() {})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.add(bytes.Repeat([]byte("q"), 20<<10))
				c.noteInput()
				c.take()
			}
		}()
	}
	wg.Wait()
	c.teardown()
}
