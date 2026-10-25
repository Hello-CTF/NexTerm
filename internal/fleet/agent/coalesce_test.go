package agent

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
)

type scriptedReader struct {
	chunks [][]byte
	reads  atomic.Int32
	gateAt int32
	gate   chan struct{}
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	index := int(r.reads.Add(1)) - 1
	if r.gate != nil && r.reads.Load() == r.gateAt {
		close(r.gate)
	}
	if index >= len(r.chunks) {
		return 0, io.EOF
	}
	return copy(p, r.chunks[index]), nil
}

func TestCoalescePumpMergesOnlyWhatIsAlreadyRead(t *testing.T) {
	var chunks [][]byte
	var want []byte
	for i := 0; i < 20; i++ {
		chunk := bytes.Repeat([]byte{byte('a' + i)}, 100+i)
		chunks = append(chunks, chunk)
		want = append(want, chunk...)
	}
	source := &scriptedReader{chunks: chunks, gateAt: 5, gate: make(chan struct{})}
	var got []byte
	var sizes []int
	first := true
	err := coalescePump(source, 4096, 32*1024, func(message []byte) error {
		if first {
			// Hold the first send until the reader has produced more chunks: they pile up and must be merged.
			first = false
			<-source.gate
		}
		got = append(got, message...)
		sizes = append(sizes, len(message))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("coalesced stream differs from the source stream")
	}
	if len(sizes) >= len(chunks) {
		t.Fatalf("sent %d messages for %d chunks, expected merging", len(sizes), len(chunks))
	}
	for _, size := range sizes {
		if size > 32*1024 {
			t.Fatalf("merged message of %d bytes exceeds the limit", size)
		}
	}
}

func TestCoalescePumpDoesNotDelayAvailableData(t *testing.T) {
	reader, writer := io.Pipe()
	delivered := make(chan []byte, 4)
	done := make(chan error, 1)
	go func() {
		done <- coalescePump(reader, 4096, 32*1024, func(message []byte) error {
			delivered <- append([]byte(nil), message...)
			return nil
		})
	}()
	for _, text := range []string{"x", "yy", "zzz"} {
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
		// Each chunk is delivered while the source stays open and silent: no timer-based batching.
		if got := string(<-delivered); got != text {
			t.Fatalf("delivered %q, want %q", got, text)
		}
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCoalescePumpPassesLargeChunksThroughIntact(t *testing.T) {
	large := bytes.Repeat([]byte("L"), 40*1024)
	small := []byte("tail")
	source := &scriptedReader{chunks: [][]byte{large, small}}
	var sizes []int
	var got []byte
	if err := coalescePump(source, 64*1024, 32*1024, func(message []byte) error {
		sizes = append(sizes, len(message))
		got = append(got, message...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(append([]byte(nil), large...), small...)) || sizes[0] != len(large) {
		t.Fatalf("large chunk altered, sizes %v", sizes)
	}
}
