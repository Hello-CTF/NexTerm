package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestProducerGracefulCloseDrainsOnLateBind(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte{0, 0xff, 1, 2}
	if err := producer.SendBinary(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
	binary[0] = 99
	terminal := json.RawMessage(`{"type":"done","seq":2}`)
	if err := producer.SendJSON(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(context.Background(), []byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("producer send after graceful close = %v, want ErrClosed", err)
	}
	if err := h.SendBinary(context.Background(), "ai", []byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("hub send after graceful close = %v, want ErrClosed", err)
	}
	if _, err := h.Producer("ai"); !errors.Is(err, ErrClosed) {
		t.Fatalf("replacement producer after graceful close = %v, want ErrClosed", err)
	}

	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	first := receiveFrame(t, receiver)
	if first.Kind != FrameBinary || first.Sequence != 1 || !bytes.Equal(first.Data, []byte{0, 0xff, 1, 2}) {
		t.Fatalf("first frame changed during graceful close: %+v", first)
	}
	second := receiveFrame(t, receiver)
	if second.Kind != FrameJSON || second.Sequence != 2 || !bytes.Equal(second.Data, terminal) {
		t.Fatalf("terminal frame changed during graceful close: %+v", second)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("next after drain = %v, want ErrClosed", err)
	}
	requireChannelCollected(t, h, "ai")
}

func TestProducerGracefulCloseReconnectDrainsExactlyOnce(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{[]byte("delta"), []byte("done")} {
		if err := producer.SendBinary(context.Background(), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	first := receiveFrame(t, receiver)
	if first.Sequence != 1 || string(first.Data) != "delta" {
		t.Fatalf("current receiver frame = %+v", first)
	}
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}

	reconnected, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	terminal := receiveFrame(t, reconnected)
	if terminal.Sequence != 2 || string(terminal.Data) != "done" {
		t.Fatalf("reconnected receiver frame = %+v", terminal)
	}
	if err := nextError(reconnected); !errors.Is(err, ErrClosed) {
		t.Fatalf("next after reconnect drain = %v, want ErrClosed", err)
	}
	if err := reconnected.Close(); err != nil {
		t.Fatal(err)
	}
	requireChannelCollected(t, h, "ai")
}

func TestProducerGracefulCloseEmptyWakesReceiver(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		frame Frame
		err   error
	}
	next := make(chan result, 1)
	go func() {
		frame, err := receiver.Next(context.Background())
		next <- result{frame: frame, err: err}
	}()
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-next:
		if !errors.Is(result.err, ErrClosed) {
			t.Fatalf("blocked next after empty graceful close = %+v, %v", result.frame, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("empty graceful close left receiver blocked")
	}
	requireChannelCollected(t, h, "ai")
}

func TestProducerConcurrentSendAndGracefulCloseDeliversAcceptedFrames(t *testing.T) {
	const (
		iterations = 25
		senders    = 16
	)
	h := New(Options{QueueFrames: senders})
	t.Cleanup(func() { _ = h.Close() })
	type sendResult struct {
		data string
		err  error
	}
	for iteration := 0; iteration < iterations; iteration++ {
		channelID := fmt.Sprintf("ai-%d", iteration)
		producer, err := h.Producer(channelID)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan sendResult, senders)
		closed := make(chan error, 1)
		for sender := 0; sender < senders; sender++ {
			data := fmt.Sprintf("event-%d", sender)
			go func() {
				<-start
				results <- sendResult{data: data, err: producer.SendBinary(context.Background(), []byte(data))}
			}()
		}
		go func() {
			<-start
			closed <- producer.CloseGracefully()
		}()
		close(start)
		accepted := make(map[string]struct{})
		for range senders {
			result := <-results
			if result.err == nil {
				accepted[result.data] = struct{}{}
				continue
			}
			if !errors.Is(result.err, ErrClosed) {
				t.Fatalf("concurrent send error = %v, want ErrClosed", result.err)
			}
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if len(accepted) == 0 {
			requireChannelCollected(t, h, channelID)
			continue
		}
		receiver, err := h.Bind(channelID)
		if err != nil {
			t.Fatal(err)
		}
		acceptedCount := len(accepted)
		for index := 0; index < acceptedCount; index++ {
			frame := receiveFrame(t, receiver)
			if frame.Sequence != uint64(index+1) {
				t.Fatalf("frame %d sequence = %d", index, frame.Sequence)
			}
			data := string(frame.Data)
			if _, ok := accepted[data]; !ok {
				t.Fatalf("frame %d was not accepted or was duplicated: %+v", index, frame)
			}
			delete(accepted, data)
		}
		if err := nextError(receiver); !errors.Is(err, ErrClosed) {
			t.Fatalf("next after concurrent drain = %v, want ErrClosed", err)
		}
		if err := receiver.Close(); err != nil {
			t.Fatal(err)
		}
		requireChannelCollected(t, h, channelID)
	}
}

func TestProducerGracefulCloseReleasesFullQueueSender(t *testing.T) {
	backpressured := make(chan struct{}, 1)
	h := New(Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		backpressured <- struct{}{}
	}})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(context.Background(), []byte("terminal")); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- producer.SendBinary(context.Background(), []byte("late")) }()
	awaitSignal(t, backpressured)
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := awaitError(t, blocked); !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked send after graceful close = %v, want ErrClosed", err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	frame := receiveFrame(t, receiver)
	if frame.Sequence != 1 || string(frame.Data) != "terminal" {
		t.Fatalf("accepted full-queue frame = %+v", frame)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("next after full-queue drain = %v, want ErrClosed", err)
	}
	requireChannelCollected(t, h, "ai")
}

func TestProducerCancelledSendAndForceCloseInterruptDrain(t *testing.T) {
	backpressured := make(chan struct{}, 1)
	h := New(Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		backpressured <- struct{}{}
	}})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(context.Background(), []byte("queued")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan error, 1)
	go func() { blocked <- producer.SendBinary(ctx, []byte("cancelled")) }()
	awaitSignal(t, backpressured)
	cancel()
	if err := awaitError(t, blocked); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked send cancellation = %v, want context.Canceled", err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	requireChannelRetained(t, h, "ai")
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("receiver after force close = %v, want ErrClosed", err)
	}
	requireChannelCollected(t, h, "ai")
}

func TestProducerCloseKeepsImmediateDropBehavior(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("terminal")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(context.Background(), []byte("drop")); err != nil {
		t.Fatal(err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("receiver after default close = %v, want ErrClosed", err)
	}
	requireChannelCollected(t, h, "terminal")
}

func TestStaleProducerGracefulCloseDoesNotCloseReplacement(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	stale, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	current, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := current.SendBinary(context.Background(), []byte("current")); err != nil {
		t.Fatalf("stale graceful close affected replacement: %v", err)
	}
	if err := current.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, receiver); string(frame.Data) != "current" {
		t.Fatalf("replacement frame = %+v", frame)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("next after replacement drain = %v, want ErrClosed", err)
	}
}

func TestDiscardPendingFinalizesDrainingChannel(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(context.Background(), []byte("discard")); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := h.DiscardPending("ai"); err != nil {
		t.Fatal(err)
	}
	if err := nextError(receiver); !errors.Is(err, ErrClosed) {
		t.Fatalf("receiver after discarding drain backlog = %v, want ErrClosed", err)
	}
	requireChannelCollected(t, h, "ai")
}

func nextError(receiver *Receiver) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := receiver.Next(ctx)
	return err
}

func awaitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("operation did not unblock")
		return nil
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("operation did not reach backpressure")
	}
}

func requireChannelCollected(t *testing.T, h *Hub, channelID string) {
	t.Helper()
	h.mu.Lock()
	_, retained := h.channels[channelID]
	h.mu.Unlock()
	if retained {
		t.Fatalf("channel %q remained retained after close", channelID)
	}
}

func requireChannelRetained(t *testing.T, h *Hub, channelID string) {
	t.Helper()
	h.mu.Lock()
	_, retained := h.channels[channelID]
	h.mu.Unlock()
	if !retained {
		t.Fatalf("channel %q was collected before its backlog drained", channelID)
	}
}
