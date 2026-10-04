package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackpressureNotificationResetsAfterDrain(t *testing.T) {
	var calls atomic.Int32
	notified := make(chan int, 2)
	h := New(Options{
		QueueFrames: 1,
		OnBackpressure: func(_ string, queuedBytes int) {
			calls.Add(1)
			notified <- queuedBytes
		},
	})
	if err := h.SendBinary(context.Background(), "terminal", []byte("first")); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- h.SendBinary(context.Background(), "terminal", []byte("second")) }()
	select {
	case queuedBytes := <-notified:
		if queuedBytes == 0 {
			t.Fatal("backpressure notification had zero queued bytes")
		}
	case <-time.After(time.Second):
		t.Fatal("missing backpressure notification")
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	receiveFrame(t, receiver)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	receiveFrame(t, receiver)

	if err := h.SendBinary(context.Background(), "terminal", []byte("third")); err != nil {
		t.Fatal(err)
	}
	go func() { blocked <- h.SendBinary(context.Background(), "terminal", []byte("fourth")) }()
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("drained queue did not reset backpressure episode")
	}
	receiveFrame(t, receiver)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	receiveFrame(t, receiver)
	if got := calls.Load(); got != 2 {
		t.Fatalf("backpressure callback count = %d, want one per episode", got)
	}
}

func TestBackpressureDrainNotificationPairsWithEntry(t *testing.T) {
	entries := make(chan int, 4)
	drains := make(chan int, 4)
	h := New(Options{
		QueueFrames:    4,
		OnBackpressure: func(_ string, queuedBytes int) { entries <- queuedBytes },
		OnDrain:        func(_ string, queuedBytes int) { drains <- queuedBytes },
	})
	ctx := context.Background()
	for range 4 {
		if err := h.SendBinary(ctx, "terminal", []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}
	blocked := make(chan error, 1)
	go func() { blocked <- h.SendBinary(ctx, "terminal", []byte("12345")) }()
	select {
	case queuedBytes := <-entries:
		if queuedBytes == 0 {
			t.Fatal("backpressure entry had zero queued bytes")
		}
	case <-time.After(time.Second):
		t.Fatal("missing backpressure entry notification")
	}
	select {
	case queuedBytes := <-drains:
		t.Fatalf("drain fired while backlog still present: %d", queuedBytes)
	case <-time.After(50 * time.Millisecond):
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	for range 4 {
		receiveFrame(t, receiver)
	}
	select {
	case queuedBytes := <-drains:
		if queuedBytes != 5 {
			t.Fatalf("drain queued bytes = %d, want remaining single frame", queuedBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("missing drain notification after real drain")
	}
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	receiveFrame(t, receiver)
	for range 4 {
		if err := h.SendBinary(ctx, "terminal", []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}
	go func() { blocked <- h.SendBinary(ctx, "terminal", []byte("12345")) }()
	select {
	case <-entries:
	case <-time.After(time.Second):
		t.Fatal("second episode did not re-arm backpressure entry")
	}
	select {
	case <-drains:
		t.Fatal("second episode drained without receiver acks")
	case <-time.After(50 * time.Millisecond):
	}
	receiveFrame(t, receiver)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
}
