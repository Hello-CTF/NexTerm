package hub

import (
	"context"
	"testing"
)

func TestInFlightFrameReplaysFirstOnRebind(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	ctx := context.Background()
	if err := h.SendBinary(ctx, "terminal", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := h.SendBinary(ctx, "terminal", []byte("second")); err != nil {
		t.Fatal(err)
	}
	first, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := first.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(frame.Data) != "first" {
		t.Fatalf("frame = %q", frame.Data)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	rebound, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer rebound.Close()
	if frame := receiveFrame(t, rebound); string(frame.Data) != "first" || frame.Sequence != 1 {
		t.Fatalf("replayed frame = %+v", frame)
	}
	if frame := receiveFrame(t, rebound); string(frame.Data) != "second" || frame.Sequence != 2 {
		t.Fatalf("following frame = %+v", frame)
	}
}

func TestAckIgnoresMismatchedSequence(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	ctx := context.Background()
	if err := h.SendBinary(ctx, "terminal", []byte("first")); err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(frame.Sequence + 1); err != nil {
		t.Fatal(err)
	}
	again, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sequence != frame.Sequence {
		t.Fatalf("mismatched ack dropped the frame: %+v vs %+v", again, frame)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	stats := h.Stats()
	if stats.QueuedFrames != 0 || stats.QueuedBytes != 0 {
		t.Fatalf("frames left after ack: %+v", stats)
	}
}

func TestDrainingChannelRetainedUntilInFlightAck(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	ctx := context.Background()
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendBinary(ctx, []byte("terminal")); err != nil {
		t.Fatal(err)
	}
	if err := producer.CloseGracefully(); err != nil {
		t.Fatal(err)
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := receiver.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(frame.Data) != "terminal" {
		t.Fatalf("frame = %q", frame.Data)
	}
	requireChannelRetained(t, h, "ai")
	if _, err := receiver.Next(ctx); err != nil {
		t.Fatalf("in-flight frame was not re-served: %v", err)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	requireChannelCollected(t, h, "ai")
	if err := nextError(receiver); err != ErrClosed {
		t.Fatalf("next after final ack = %v, want ErrClosed", err)
	}
}

func TestStaleReceiverAckDoesNotPopNewCheckout(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	ctx := context.Background()
	if err := h.SendBinary(ctx, "terminal", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := h.SendBinary(ctx, "terminal", []byte("second")); err != nil {
		t.Fatal(err)
	}
	stale, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	first, err := stale.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if _, err := current.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stale.Ack(first.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := current.Ack(first.Sequence); err != nil {
		t.Fatal(err)
	}
	frame := receiveFrame(t, current)
	if string(frame.Data) != "second" {
		t.Fatalf("frame after double ack = %q, want second", frame.Data)
	}
}
