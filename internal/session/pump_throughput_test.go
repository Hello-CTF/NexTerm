package session

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPumpThroughputLogsDeltasUntilContextEnds(t *testing.T) {
	previous := pumpThroughputEvery
	pumpThroughputEvery = 10 * time.Millisecond
	t.Cleanup(func() { pumpThroughputEvery = previous })
	out := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var bytesSeen, chunks atomic.Int64
	bytesSeen.Store(4096)
	chunks.Store(3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		logPumpThroughput(ctx, logger, "s1", "t1", &bytesSeen, &chunks)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "tab output throughput") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	line := out.String()
	for _, want := range []string{"tab output throughput", "session=s1", "tab=t1", "bytes=4096", "chunks=3"} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in %q", want, line)
		}
	}
}
