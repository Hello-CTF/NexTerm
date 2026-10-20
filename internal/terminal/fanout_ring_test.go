package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"
)

// gatedSequenceSink retains exactly one frame in SendBinary until the test
// releases that exact sequence, so a subscriber can be kept a fixed number of
// frames behind the publisher without any timing dependence.
type gatedSequenceSink struct {
	entered chan uint64
	release chan uint64

	mu     sync.Mutex
	frames [][]byte
}

func newGatedSequenceSink() *gatedSequenceSink {
	return &gatedSequenceSink{entered: make(chan uint64), release: make(chan uint64)}
}

func (s *gatedSequenceSink) SendBinary(ctx context.Context, data []byte) error {
	seq := binary.BigEndian.Uint64(data)
	s.entered <- seq
	select {
	case released := <-s.release:
		if released != seq {
			return fmt.Errorf("released frame %d while sending frame %d", released, seq)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.frames = append(s.frames, bytes.Clone(data))
	s.mu.Unlock()
	return nil
}

// SendJSON records the frame like SendBinary; the frame kind is identified
// by content in ordering assertions.
func (s *gatedSequenceSink) SendJSON(ctx context.Context, data []byte) error {
	return s.SendBinary(ctx, data)
}

func (s *gatedSequenceSink) frame(seq uint64) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq >= uint64(len(s.frames)) {
		return nil
	}
	return s.frames[seq]
}

func (s *gatedSequenceSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func fanoutSequenceFrame(seq uint64) []byte {
	frame := make([]byte, 16)
	binary.BigEndian.PutUint64(frame, seq)
	copy(frame[8:], "payload!")
	return frame
}

func fanoutQueueCapacity(t *testing.T, f *Fanout, id string) int {
	t.Helper()
	f.mu.Lock()
	entry := f.sinks[id]
	f.mu.Unlock()
	if entry == nil {
		t.Fatalf("subscriber %q is not attached", id)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return cap(entry.queue)
}

// TestFanoutRetainedFramesKeepBoundedQueueMetadata publishes 10000 frames to a
// subscriber that is deterministically kept one frame in flight plus a minimal
// retained backlog, and verifies frame order, lag accounting, and that the
// queue metadata stays bounded by the configured frame capacity.
func TestFanoutRetainedFramesKeepBoundedQueueMetadata(t *testing.T) {
	const frames = 10000
	const queueFrames = 8
	f := newFanoutSlowTest(FanoutOptions{QueueFrames: queueFrames, QueueBytes: 1 << 20})
	sink := newGatedSequenceSink()
	f.Attach("slow", sink)
	t.Cleanup(func() { f.DetachAll() })

	send := func(seq uint64) {
		t.Helper()
		if !f.Send(context.Background(), fanoutSequenceFrame(seq)) {
			t.Fatalf("frame %d was not accepted", seq)
		}
	}
	entered := func(seq uint64) {
		t.Helper()
		select {
		case got := <-sink.entered:
			if got != seq {
				t.Fatalf("sink entered with frame %d, want frame %d", got, seq)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for sink to enter frame %d", seq)
		}
	}
	release := func(seq uint64) {
		t.Helper()
		sink.release <- seq
	}

	send(1)
	entered(1)
	send(2)
	send(3)
	// Steady state: one frame in flight, two frames retained in the queue.
	// Releasing frame seq lets the sink pick up seq+1 while seq+2 is already
	// retained, then seq+3 is published; the backlog never drains.
	for seq := uint64(1); seq <= frames-3; seq++ {
		release(seq)
		entered(seq + 1)
		send(seq + 3)
	}

	stats, ok := f.SubscriberStats("slow")
	if !ok {
		t.Fatal("slow subscriber is gone")
	}
	if stats.LagFrames != 3 || stats.LagBytes != 3*len(fanoutSequenceFrame(0)) {
		t.Fatalf("steady-state lag = %+v, want 3 queued frames", stats)
	}
	if stats.DroppedFrames != 0 || stats.DroppedBytes != 0 || stats.Gaps != 0 {
		t.Fatalf("steady-state stats report loss: %+v", stats)
	}
	if capacity := fanoutQueueCapacity(t, f, "slow"); capacity > queueFrames {
		t.Fatalf("queue capacity = %d after %d frames, want <= %d", capacity, frames, queueFrames)
	}

	release(frames - 2)
	entered(frames - 1)
	release(frames - 1)
	entered(frames)
	release(frames)

	waitFor(t, "all 10000 frames delivered", func() bool { return sink.count() == frames })
	for seq := uint64(1); seq <= frames; seq++ {
		if got, want := sink.frame(seq-1), fanoutSequenceFrame(seq); !bytes.Equal(got, want) {
			t.Fatalf("delivered frame %d = %v, want %v", seq, got, want)
		}
	}
	// The sink records the final frame before SendBinary returns, while lag is
	// only zeroed by finish afterwards; wait for that post-finish state instead
	// of assuming it. Lag stays nonzero from the first publish until then.
	waitFor(t, "lag drained after final frame", func() bool {
		stats, ok := f.SubscriberStats("slow")
		return ok && stats.LagFrames == 0 && stats.LagBytes == 0
	})
	stats, ok = f.SubscriberStats("slow")
	if !ok {
		t.Fatal("slow subscriber detached after drain")
	}
	if stats.LagFrames != 0 || stats.LagBytes != 0 || stats.DroppedFrames != 0 || stats.DroppedBytes != 0 || stats.Gaps != 0 {
		t.Fatalf("final stats = %+v, want an idle subscriber without loss", stats)
	}
}
