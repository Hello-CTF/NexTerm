package ssh

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPaceWriterHoldsAverageRateAndPassesDataThrough(t *testing.T) {
	var sink bytes.Buffer
	writer := PaceWriter(context.Background(), &sink, 1000)
	start := time.Now()
	if _, err := writer.Write(make([]byte, 150)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 140*time.Millisecond {
		t.Fatalf("150 bytes at 1000 B/s returned after %v, want at least ~150ms", elapsed)
	}
	if sink.Len() != 150 {
		t.Fatalf("sink got %d bytes", sink.Len())
	}
}

func TestPaceWriterWithoutLimitIsTransparent(t *testing.T) {
	var sink bytes.Buffer
	if got := PaceWriter(context.Background(), &sink, 0); got != &sink {
		t.Fatal("an unset limit must not wrap the writer")
	}
}

func TestPaceWriterStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var sink bytes.Buffer
	if _, err := PaceWriter(ctx, &sink, 1).Write(make([]byte, 100)); err == nil {
		t.Fatal("canceled pacing must return an error instead of sleeping")
	}
}
