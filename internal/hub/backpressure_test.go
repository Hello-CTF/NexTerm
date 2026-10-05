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

func TestBackpressureDrainCallbackPrecedesProducerWake(t *testing.T) {
	drainStarted := make(chan struct{}, 1)
	drainRelease := make(chan struct{})
	entries := make(chan int, 4)
	drained := make(chan int, 1)
	h := New(Options{
		QueueFrames:    4,
		OnBackpressure: func(_ string, queuedBytes int) { entries <- queuedBytes },
		OnDrain: func(_ string, queuedBytes int) {
			drainStarted <- struct{}{}
			<-drainRelease
			drained <- queuedBytes
		},
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
	case <-entries:
	case <-time.After(time.Second):
		t.Fatal("missing backpressure entry")
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
	acksDone := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				acksDone <- err
				return
			}
			if err := receiver.Ack(frame.Sequence); err != nil {
				acksDone <- err
				return
			}
		}
		acksDone <- nil
	}()
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("drain callback did not start")
	}

	sent := make(chan error, 1)
	go func() { sent <- h.SendBinary(ctx, "terminal", []byte("12345")) }()
	select {
	case <-sent:
		t.Fatal("producer completed while the drain callback was still blocked")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-entries:
		t.Fatal("new episode entered before the old drain completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(drainRelease)
	if err := <-acksDone; err != nil {
		t.Fatal(err)
	}
	select {
	case queuedBytes := <-drained:
		if queuedBytes != 5 {
			t.Fatalf("drain queued bytes = %d, want remaining single frame", queuedBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("drain callback did not complete")
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.SendBinary(ctx, "terminal", []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}
	blocked = make(chan error, 1)
	go func() { blocked <- h.SendBinary(ctx, "terminal", []byte("12345")) }()
	select {
	case <-entries:
	case <-time.After(time.Second):
		t.Fatal("new episode did not enter after the old drain completed")
	}
	select {
	case <-drained:
		t.Fatal("second drain fired without a new drain transition")
	case <-time.After(50 * time.Millisecond):
	}
	receiveFrame(t, receiver)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
}

func TestBackpressureEntryCallbackPrecedesDrain(t *testing.T) {
	entryStarted := make(chan struct{}, 1)
	entryRelease := make(chan struct{})
	entries := make(chan int, 1)
	drained := make(chan int, 1)
	h := New(Options{
		QueueFrames: 4,
		OnBackpressure: func(_ string, queuedBytes int) {
			entryStarted <- struct{}{}
			<-entryRelease
			entries <- queuedBytes
		},
		OnDrain: func(_ string, queuedBytes int) { drained <- queuedBytes },
	})
	ctx := context.Background()
	for range 4 {
		if err := h.SendBinary(ctx, "terminal", []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}
	receiver, err := h.Bind("terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	blocked := make(chan error, 1)
	go func() { blocked <- h.SendBinary(ctx, "terminal", []byte("12345")) }()
	select {
	case <-entryStarted:
	case <-time.After(time.Second):
		t.Fatal("entry callback did not start")
	}

	ackDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		frame, err := receiver.Next(ctx)
		cancel()
		if err != nil {
			ackDone <- err
			return
		}
		ackDone <- receiver.Ack(frame.Sequence)
	}()
	select {
	case <-ackDone:
		t.Fatal("drain path completed while the entry callback was still blocked")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-drained:
		t.Fatal("drain fired before the entry callback completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(entryRelease)
	select {
	case queuedBytes := <-entries:
		if queuedBytes == 0 {
			t.Fatal("entry had zero queued bytes")
		}
	case <-time.After(time.Second):
		t.Fatal("entry callback did not complete")
	}
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		receiveFrame(t, receiver)
	}
	select {
	case queuedBytes := <-drained:
		if queuedBytes != 5 {
			t.Fatalf("drain queued bytes = %d, want remaining single frame", queuedBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("missing drain after the entry completed")
	}
	receiveFrame(t, receiver)
	if stats := h.Stats(); stats.QueuedFrames != 0 {
		t.Fatalf("final queued frames = %d, want 0", stats.QueuedFrames)
	}
	select {
	case <-entries:
		t.Fatal("unexpected second entry")
	case <-drained:
		t.Fatal("unexpected second drain")
	case <-time.After(50 * time.Millisecond):
	}
}
