package terminal

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

// orderSink records frames in delivery order, holding each send until the
// test releases it, so the fanout's enqueue order and delivery order can be
// compared deterministically.
type orderSink struct {
	mu      sync.Mutex
	frames  [][]byte
	entered chan struct{}
	release chan struct{}
}

func newOrderSink() *orderSink {
	return &orderSink{entered: make(chan struct{}, 8), release: make(chan struct{}, 8)}
}

func (s *orderSink) SendBinary(_ context.Context, data []byte) error {
	s.entered <- struct{}{}
	<-s.release
	s.mu.Lock()
	s.frames = append(s.frames, bytes.Clone(data))
	s.mu.Unlock()
	return nil
}

func (s *orderSink) SendJSON(ctx context.Context, data []byte) error {
	return s.SendBinary(ctx, data)
}

func (s *orderSink) allow() {
	s.release <- struct{}{}
}

func (s *orderSink) recorded() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.frames))
	copy(out, s.frames)
	return out
}

// A recovery anchor must travel the same ordered queue as binary output: it
// goes out only after the frames already queued for the subscriber, and the
// replay follows it — never anchor-then-stale-frames-then-replay.
func TestFanoutJSONFrameOrderedBehindQueuedBinary(t *testing.T) {
	f := NewFanout()
	sink := newOrderSink()
	f.Attach("s", sink)
	f.SendTo("s", []byte("b"))
	<-sink.entered // the worker holds frame b until released
	f.SendTo("s", []byte("c"))
	// Recovery: the anchor (JSON) then the replay, enqueued in order.
	f.SendJSONTo("s", []byte(`{"type":"nexterm.stream-anchor","epoch":"recovery-t","streamSeq":1}`))
	f.SendTo("s", []byte("bc"))
	for range 4 {
		sink.allow()
	}
	waitFor(t, "four delivered frames", func() bool { return len(sink.recorded()) == 4 })
	got := sink.recorded()
	if string(got[0]) != "b" || string(got[1]) != "c" ||
		!bytes.Contains(got[2], []byte("nexterm.stream-anchor")) || string(got[3]) != "bc" {
		t.Fatalf("delivery order = %q", got)
	}
}
