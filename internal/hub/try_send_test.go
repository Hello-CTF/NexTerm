package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTrySendJSONReportsFullWithoutBlocking(t *testing.T) {
	h := New(Options{QueueFrames: 1})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySendJSON(context.Background(), []byte(`{"seq":1}`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- producer.TrySendJSON(context.Background(), []byte(`{"seq":2}`)) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("try-send on full queue = %v, want ErrQueueFull", err)
		}
	case <-time.After(time.Second):
		t.Fatal("try-send blocked on a full queue")
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiveFrame(t, receiver)
	if err := producer.TrySendJSON(context.Background(), []byte(`{"seq":3}`)); err != nil {
		t.Fatalf("try-send after drain = %v", err)
	}
	receiveFrame(t, receiver)
}

func TestTrySendJSONValidatesAndDetectsClosed(t *testing.T) {
	h := New(Options{})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySendJSON(context.Background(), []byte(`{`)); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("invalid JSON = %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySendJSON(context.Background(), []byte(`{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("try-send on closed channel = %v", err)
	}
}

func TestTrySendBinaryFullAndByteBound(t *testing.T) {
	h := New(Options{QueueFrames: 8, QueueBytes: 4})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySendBinary(context.Background(), []byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySendBinary(context.Background(), []byte("5678")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("byte-bound try-send = %v, want ErrQueueFull", err)
	}
}

func TestBlockingSendStillWaitsForDrain(t *testing.T) {
	h := New(Options{QueueFrames: 1})
	t.Cleanup(func() { _ = h.Close() })
	producer, err := h.Producer("ai")
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.SendJSON(context.Background(), []byte(`{"seq":1}`)); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- producer.SendJSON(context.Background(), []byte(`{"seq":2}`)) }()
	select {
	case err := <-blocked:
		t.Fatalf("blocking send returned early with %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	receiver, err := h.Bind("ai")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiveFrame(t, receiver)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	receiveFrame(t, receiver)
}
