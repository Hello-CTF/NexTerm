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
