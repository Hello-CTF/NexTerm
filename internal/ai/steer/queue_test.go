package steer

import (
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestQueueFIFOAndDrain(t *testing.T) {
	queue := NewQueue(4)
	for _, text := range []string{"first", "second", "third"} {
		if err := queue.Push(schema.UserMessage(text)); err != nil {
			t.Fatalf("Push(%q): %v", text, err)
		}
	}
	drained := queue.Drain()
	if len(drained) != 3 || drained[0].Content != "first" || drained[1].Content != "second" || drained[2].Content != "third" {
		t.Fatalf("Drain order = %#v", drained)
	}
	if again := queue.Drain(); len(again) != 0 {
		t.Fatalf("second Drain = %#v, want empty", again)
	}
}

func TestQueueBoundedAndReusableAfterDrain(t *testing.T) {
	queue := NewQueue(2)
	if err := queue.Push(schema.UserMessage("one")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(schema.UserMessage("two")); err != nil {
		t.Fatal(err)
	}
	if err := queue.Push(schema.UserMessage("three")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third Push error = %v, want ErrQueueFull", err)
	}
	_ = queue.Drain()

	if err := queue.Push(schema.UserMessage("four")); err != nil {
		t.Fatalf("Push after Drain: %v", err)
	}
	if err := queue.Push(nil); err == nil {
		t.Fatal("nil message accepted")
	}
	if err := queue.Push(schema.UserMessage("five")); err != nil {
		t.Fatalf("Push to refill limit: %v", err)
	}
	if err := queue.Push(schema.UserMessage("six")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Push over limit error = %v, want ErrQueueFull", err)
	}
}

func TestQueueConcurrentPushAndDrain(t *testing.T) {
	queue := NewQueue(32)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := queue.Push(schema.UserMessage("race")); err != nil && !errors.Is(err, ErrQueueFull) {
				t.Errorf("unexpected Push error: %v", err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = queue.Drain()
		}()
	}
	wg.Wait()

	if drained := len(queue.Drain()); drained > 32 {
		t.Fatalf("Drain returned %d, exceeds limit", drained)
	}
	if again := queue.Drain(); len(again) != 0 {
		t.Fatalf("second Drain = %#v, want empty", again)
	}
}
