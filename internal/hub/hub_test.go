package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingFramesSurviveBindAndKeepMixedOrder(t *testing.T) {
	h := New(Options{})
	ctx := context.Background()
	binary := []byte{0, 0xff, 1, 2}
	if err := h.SendBinary(ctx, "terminal", binary); err != nil {
		t.Fatal(err)
	}
	binary[0] = 99
	if err := h.SendJSON(ctx, "terminal", json.RawMessage(`{"event":"delta"}`)); err != nil {
		t.Fatal(err)
	}
	stats := h.Stats()
	if stats.PendingChannels != 1 || stats.QueuedFrames != 2 {
		t.Fatalf("unexpected pending stats: %+v", stats)
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	first := receiveFrame(t, receiver)
	if first.Kind != FrameBinary || first.Sequence != 1 || !bytes.Equal(first.Data, []byte{0, 0xff, 1, 2}) {
		t.Fatalf("binary frame changed or reordered: %+v", first)
	}
	second := receiveFrame(t, receiver)
	if second.Kind != FrameJSON || second.Sequence != 2 || string(second.Data) != `{"event":"delta"}` {
		t.Fatalf("JSON frame changed or reordered: %+v", second)
	}
}

func TestRebindInvalidatesOnlyOldReceiver(t *testing.T) {
	var closed atomic.Int32
	h := New(Options{OnChannelClose: func(string) { closed.Add(1) }})
	oldReceiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	newReceiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldReceiver.Next(context.Background()); !errors.Is(err, ErrReplaced) {
		t.Fatalf("old receiver error = %v, want ErrReplaced", err)
	}
	if err := oldReceiver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := closed.Load(); got != 0 {
		t.Fatalf("stale close detached a new receiver %d times", got)
	}
	if err := h.SendBinary(context.Background(), "terminal", []byte("current")); err != nil {
		t.Fatal(err)
	}
	if frame := receiveFrame(t, newReceiver); string(frame.Data) != "current" {
		t.Fatalf("new receiver lost current frame: %+v", frame)
	}
	if err := newReceiver.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newReceiver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("close callback count = %d, want exactly one", got)
	}
}

func TestBackpressureBlocksUntilDrainedAndHonorsCancellation(t *testing.T) {
	h := New(Options{QueueFrames: 1})
	ctx := context.Background()
	if err := h.SendBinary(ctx, "terminal", []byte("first")); err != nil {
		t.Fatal(err)
	}
	sendDone := make(chan error, 1)
	go func() { sendDone <- h.SendBinary(context.Background(), "terminal", []byte("second")) }()
	select {
	case err := <-sendDone:
		t.Fatalf("full queue accepted a second frame: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	if frame := receiveFrame(t, receiver); string(frame.Data) != "first" {
		t.Fatalf("first frame = %q", frame.Data)
	}
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("draining did not release blocked sender")
	}
	if frame := receiveFrame(t, receiver); string(frame.Data) != "second" || frame.Sequence != 2 {
		t.Fatalf("second frame = %+v", frame)
	}

	if err := h.SendBinary(ctx, "full", []byte("first")); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := h.SendBinary(cancelCtx, "full", []byte("blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked send error = %v, want deadline", err)
	}
}

func TestCloseChannelReleasesQueuesAndBlockedOperations(t *testing.T) {
	h := New(Options{QueueFrames: 1})
	if err := h.SendBinary(context.Background(), "terminal", []byte("pending")); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- h.SendBinary(context.Background(), "terminal", []byte("blocked")) }()
	time.Sleep(20 * time.Millisecond)
	if err := h.CloseChannel("terminal"); err != nil {
		t.Fatal(err)
	}
	if err := <-blocked; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked sender error = %v, want ErrClosed", err)
	}
	stats := h.Stats()
	if stats.QueuedFrames != 0 || stats.QueuedBytes != 0 || stats.PendingChannels != 0 {
		t.Fatalf("close retained queued data: %+v", stats)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.SendBinary(context.Background(), "terminal", nil); !errors.Is(err, ErrHubClosed) {
		t.Fatalf("send after hub close = %v, want ErrHubClosed", err)
	}
}

func TestConcurrentClientsChannelsAndThroughput(t *testing.T) {
	h := New(Options{QueueFrames: 32, QueueBytes: 1 << 20})
	const (
		clients           = 2
		channelsPerClient = 3
		framesPerChannel  = 1000
	)
	type result struct {
		channelID string
		err       error
	}
	results := make(chan result, clients*channelsPerClient)
	var senders sync.WaitGroup
	for client := 0; client < clients; client++ {
		for stream := 0; stream < channelsPerClient; stream++ {
			channelID := fmt.Sprintf("client-%d-channel-%d", client, stream)
			receiver, err := h.Bind(channelID)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				defer receiver.Close()
				for index := 0; index < framesPerChannel; index++ {
					frame, err := receiver.Next(context.Background())
					if err != nil {
						results <- result{channelID: channelID, err: err}
						return
					}
					wantSequence := uint64(index + 1)
					wantData := fmt.Sprintf("%d", index)
					if frame.Sequence != wantSequence || string(frame.Data) != wantData {
						results <- result{channelID: channelID, err: fmt.Errorf("frame %d = %+v", index, frame)}
						return
					}
					wantKind := FrameBinary
					if index%2 == 1 {
						wantKind = FrameJSON
					}
					if frame.Kind != wantKind {
						results <- result{channelID: channelID, err: fmt.Errorf("frame %d kind = %d", index, frame.Kind)}
						return
					}
					if err := receiver.Ack(frame.Sequence); err != nil {
						results <- result{channelID: channelID, err: err}
						return
					}
				}
				results <- result{channelID: channelID}
			}()
			senders.Add(1)
			go func() {
				defer senders.Done()
				for index := 0; index < framesPerChannel; index++ {
					data := []byte(fmt.Sprintf("%d", index))
					var err error
					if index%2 == 0 {
						err = h.SendBinary(context.Background(), channelID, data)
					} else {
						err = h.SendJSON(context.Background(), channelID, json.RawMessage(data))
					}
					if err != nil {
						t.Errorf("send %s frame %d: %v", channelID, index, err)
						return
					}
				}
			}()
		}
	}
	senders.Wait()
	for index := 0; index < clients*channelsPerClient; index++ {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("%s: %v", result.channelID, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("throughput test deadlocked")
		}
	}
	stats := h.Stats()
	if stats.QueuedFrames != 0 || stats.QueuedBytes != 0 {
		t.Fatalf("frames left after drain: %+v", stats)
	}
}

func TestProducerReplacementRejectsStaleSendAndClose(t *testing.T) {
	h := New(Options{})
	oldProducer, err := h.Producer("terminal")
	if err != nil {
		t.Fatal(err)
	}
	newProducer, err := h.Producer("terminal")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldProducer.SendBinary(context.Background(), []byte("stale")); !errors.Is(err, ErrReplaced) {
		t.Fatalf("stale producer send error = %v, want ErrReplaced", err)
	}
	if err := oldProducer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newProducer.SendBinary(context.Background(), []byte("current")); err != nil {
		t.Fatalf("stale close closed current producer: %v", err)
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	frame := receiveFrame(t, receiver)
	if string(frame.Data) != "current" {
		t.Fatalf("frame = %q, want current", frame.Data)
	}
	if err := newProducer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newProducer.SendBinary(context.Background(), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed producer send error = %v", err)
	}
}

func receiveFrame(t *testing.T, receiver *Receiver) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	return frame
}
