package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"
)

type gatedFanoutSink struct {
	entered     chan struct{}
	release     chan struct{}
	returned    chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	returnOnce  sync.Once

	mu     sync.Mutex
	frames [][]byte
}

func newGatedFanoutSink() *gatedFanoutSink {
	return &gatedFanoutSink{
		entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}),
	}
}

func (s *gatedFanoutSink) SendBinary(ctx context.Context, data []byte) error {
	s.enterOnce.Do(func() { close(s.entered) })
	defer s.returnOnce.Do(func() { close(s.returned) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.frames = append(s.frames, bytes.Clone(data))
	s.mu.Unlock()
	return nil
}

func (s *gatedFanoutSink) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.frames, nil)
}

func (s *gatedFanoutSink) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func newFanoutSlowTest(options FanoutOptions) *Fanout {
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return NewFanoutWithOptions(options)
}

func waitFanoutSignal(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

func TestFanoutSlowSubscriberDoesNotBlockFastSubscriber(t *testing.T) {
	f := newFanoutSlowTest(FanoutOptions{QueueFrames: 4, QueueBytes: 1024})
	slow := newGatedFanoutSink()
	fast := &memSink{}
	f.Attach("slow", slow)
	f.Attach("fast", fast)
	t.Cleanup(func() { slow.unblock(); f.DetachAll() })

	if !f.Send(context.Background(), []byte("a")) {
		t.Fatal("initial frame was not accepted")
	}
	waitFanoutSignal(t, "slow sink barrier", slow.entered)
	for _, frame := range []string{"b", "c", "d"} {
		if !f.Send(context.Background(), []byte(frame)) {
			t.Fatalf("frame %q was not accepted while slow sink was blocked", frame)
		}
	}
	waitFor(t, "fast subscriber complete ordered stream", func() bool {
		return bytes.Equal(fast.Bytes(), []byte("abcd"))
	})
	stats, ok := f.SubscriberStats("slow")
	if !ok || stats.LagFrames == 0 || stats.Gaps != 0 || stats.DroppedFrames != 0 {
		t.Fatalf("slow subscriber stats = %+v, present %v", stats, ok)
	}
	slow.unblock()
	waitFor(t, "slow subscriber catch-up without a gap", func() bool {
		return bytes.Equal(slow.Bytes(), []byte("abcd"))
	})
}

func TestFanoutOverflowDisconnectsOnlySlowSubscriber(t *testing.T) {
	f := newFanoutSlowTest(FanoutOptions{QueueFrames: 2, QueueBytes: 1024})
	slow := newGatedFanoutSink()
	fast := &memSink{}
	type disconnect struct {
		stats FanoutSubscriberStats
		cause error
	}
	disconnected := make(chan disconnect, 1)
	f.AttachWithOptions("slow", slow, FanoutSubscriberOptions{
		OnDisconnect: func(stats FanoutSubscriberStats, cause error) {
			disconnected <- disconnect{stats: stats, cause: cause}
		},
	})
	f.Attach("fast", fast)
	t.Cleanup(func() { slow.unblock(); f.DetachAll() })

	f.Send(context.Background(), []byte("a"))
	waitFanoutSignal(t, "slow sink barrier", slow.entered)
	waitFor(t, "fast first frame before overflow", func() bool { return bytes.Equal(fast.Bytes(), []byte("a")) })
	f.Send(context.Background(), []byte("b"))
	waitFor(t, "fast second frame before overflow", func() bool { return bytes.Equal(fast.Bytes(), []byte("ab")) })
	if !f.Send(context.Background(), []byte("c")) {
		t.Fatal("fast subscriber should accept the frame that overflows slow")
	}
	var event disconnect
	select {
	case event = <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("slow subscriber overflow did not notify disconnect")
	}
	if !errors.Is(event.cause, ErrFanoutQueueFull) {
		t.Fatalf("disconnect cause = %v", event.cause)
	}
	if event.stats.Gaps != 1 || event.stats.DroppedFrames != 2 || event.stats.DroppedBytes != 2 || event.stats.LagFrames != 1 {
		t.Fatalf("disconnect stats = %+v, want one gap, queued/rejected b+c dropped, and a in flight", event.stats)
	}
	if f.Count() != 1 {
		t.Fatalf("subscriber count = %d, want only fast", f.Count())
	}
	waitFanoutSignal(t, "blocked send canceled by disconnect", slow.returned)
	waitFor(t, "fast subscriber survives slow overflow", func() bool {
		return bytes.Equal(fast.Bytes(), []byte("abc"))
	})
}

func TestFanoutPublisherP99WithBlockedSubscriber(t *testing.T) {
	const samples = 256
	f := newFanoutSlowTest(FanoutOptions{QueueFrames: samples + 2, QueueBytes: 1 << 20})
	slow := newGatedFanoutSink()
	fast := &memSink{}
	f.Attach("slow", slow)
	f.Attach("fast", fast)
	t.Cleanup(func() { slow.unblock(); f.DetachAll() })
	warm := []byte("warm")
	f.Send(context.Background(), warm)
	waitFanoutSignal(t, "slow sink barrier", slow.entered)

	payload := bytes.Repeat([]byte("p"), 128)
	durations := make([]time.Duration, 0, samples)
	for range samples {
		started := time.Now()
		if !f.Send(context.Background(), payload) {
			t.Fatal("publish was not accepted within the configured queue bound")
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(len(durations)*99)/100]
	if p99 >= 100*time.Millisecond {
		t.Fatalf("publisher P99 with a blocked subscriber = %s", p99)
	}
	t.Logf("fanout publish P99 with one barrier-blocked subscriber and one active fast subscriber (%d publishes): %s", samples, p99)
	wantBytes := len(warm) + samples*len(payload)
	waitFor(t, "fast subscriber after P99 publishes", func() bool { return fast.Len() == wantBytes })
	stats, ok := f.SubscriberStats("slow")
	if !ok || stats.Gaps != 0 || stats.DroppedFrames != 0 || stats.LagFrames != samples+1 {
		t.Fatalf("blocked subscriber stats = %+v, present %v", stats, ok)
	}
}

func TestFanoutReplacementDoesNotWaitForDisconnectCallback(t *testing.T) {
	f := newFanoutSlowTest(FanoutOptions{})
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	f.AttachWithOptions("shared", SinkFunc(func(context.Context, []byte) error {
		return errors.New("send failed")
	}), FanoutSubscriberOptions{
		OnDisconnect: func(FanoutSubscriberStats, error) {
			close(callbackStarted)
			<-releaseCallback
		},
	})
	f.Send(context.Background(), []byte("old"))
	waitFanoutSignal(t, "disconnect callback", callbackStarted)
	fresh := &memSink{}
	replaced := make(chan struct{})
	go func() {
		f.Attach("shared", fresh)
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement waited for the old disconnect callback")
	}
	close(releaseCallback)
	f.Send(context.Background(), []byte("new"))
	waitFor(t, "replacement after deferred callback", func() bool { return bytes.Equal(fresh.Bytes(), []byte("new")) })
	f.DetachAll()
}

func TestFanoutConcurrentPublishAttachDetach(t *testing.T) {
	f := newFanoutSlowTest(FanoutOptions{QueueFrames: 1024, QueueBytes: 1 << 20})
	stable := &memSink{}
	f.Attach("stable", stable)
	const publishers = 4
	const framesPerPublisher = 100
	var wg sync.WaitGroup
	for publisher := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for frame := range framesPerPublisher {
				data := []byte(fmt.Sprintf("%02d/%03d|", publisher, frame))
				if !f.Send(context.Background(), data) {
					t.Errorf("stable subscriber stopped accepting publishes")
					return
				}
			}
		}()
	}
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("dynamic-%d", worker)
			for iteration := range 50 {
				f.Attach(id, &memSink{})
				if iteration%2 == 0 {
					f.Attach(id, &memSink{})
				}
				f.Detach(id)
			}
		}()
	}
	wg.Wait()
	waitFor(t, "stable subscriber after concurrent attach/detach", func() bool {
		return len(stable.Frames()) == publishers*framesPerPublisher
	})
	f.DetachAll()
}

func TestFanoutQueuedPayloadIsOwnedBySubscriber(t *testing.T) {
	f := newFanoutSlowTest(FanoutOptions{})
	sink := &memSink{}
	f.Attach("sink", sink)
	payload := []byte("abc")
	if !f.Send(context.Background(), payload) {
		t.Fatal("payload was not accepted")
	}
	payload[0] = 'X'
	waitFor(t, "immutable queued payload", func() bool { return bytes.Equal(sink.Bytes(), []byte("abc")) })
	f.DetachAll()
}
