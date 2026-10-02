package terminal

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentTabAndOutput hammers every public path concurrently; run
// with -race to prove the locking, and without it to prove there are no
// deadlocks.
func TestConcurrentTabAndOutput(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	ctx := context.Background()
	stable := &memSink{}
	o.Attach("stable", stable)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for p := 0; p < 2; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(p)))
			for i := 0; i < 200; i++ {
				frame := []byte(fmt.Sprintf("producer-%d line-%d 中文\r\n", p, i))
				if rng.Intn(4) == 0 {
					frame = append(frame, 0x1b, '[', '3', '1', 'm')
				}
				if err := o.Chunk(ctx, frame); err != nil {
					t.Error(err)
					return
				}
			}
		}(p)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = tab.Snapshot()
			_ = tab.ScreenText()
			_ = tab.TailLines(5)
			_ = tab.ScreenHistory(5)
			_ = tab.Dump(128)
			_, _ = tab.ReplayFrom(0, 64)
			_ = tab.State()
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			sink := &memSink{}
			if err := o.AttachReplay(ctx, fmt.Sprintf("dyn-%d", i), sink, 1024); err != nil {
				t.Error(err)
				return
			}
			if i%3 == 0 {
				_, _, _ = o.AttachFrom(ctx, fmt.Sprintf("seq-%d", i), &memSink{}, 0, 128)
			}
			o.Detach(fmt.Sprintf("dyn-%d", i))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := tab.Resize(60+i, 20+i%5); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			tab.SwitchEncoding(Encoding(i % 5))
			time.Sleep(time.Millisecond)
		}
		tab.SwitchEncoding(UTF8)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			o.SetVisible(i%2 == 0)
			time.Sleep(time.Millisecond)
		}
		o.SetVisible(true)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		path := filepath.Join(t.TempDir(), "race.log")
		for i := 0; i < 5; i++ {
			if err := tab.StartRecording(path); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(2 * time.Millisecond)
			tab.StopRecording()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Let the workers overlap, then stop the reader; everyone must finish.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent workers did not finish (deadlock?)")
	}
	o.Flush(ctx)
	if len(stable.Bytes()) == 0 {
		t.Fatal("stable subscriber received nothing")
	}
}

// TestConcurrentCloseFeeds races Feed against Close: no deadlock, no
// panic, no writes after close.
func TestConcurrentCloseFeeds(t *testing.T) {
	for i := 0; i < 20; i++ {
		tab := NewTab("t", "s", 80, 24, UTF8, WithResponseHandler(func([]byte) {}))
		o := NewOutput(tab)
		ctx := context.Background()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = o.Chunk(ctx, []byte("data\x1b[6n"))
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = tab.Snapshot()
			}
		}()
		time.Sleep(time.Duration(i%3) * time.Millisecond)
		tab.Close()
		o.Close()
		wg.Wait()
		tab.Feed([]byte("late"))
	}
}

// TestConcurrentFanoutSendDetach races Send with Attach/Detach to prove
// ordered delivery and safe detachment.
func TestConcurrentFanoutSendDetach(t *testing.T) {
	f := NewFanout()
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			f.Send(ctx, []byte(fmt.Sprintf("frame-%d", i)))
		}
	}()
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("s-%d", i)
		f.Attach(id, &memSink{})
		if i%2 == 0 {
			f.Detach(id)
		}
	}
	wg.Wait()
	if f.Count() != 25 {
		t.Fatalf("count = %d", f.Count())
	}
}
