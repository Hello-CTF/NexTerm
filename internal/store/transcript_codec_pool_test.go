package store

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"
)

func codecChunks(tag string, count int) []TranscriptChunkRow {
	chunks := make([]TranscriptChunkRow, count)
	for index := range chunks {
		chunks[index] = TranscriptChunkRow{
			Seq: int64(index + 1), TS: int64(1000 + index), Kind: 1, TabID: "tab-" + tag,
			Data: []byte(fmt.Sprintf("%s line %d\r\n", tag, index)),
		}
	}
	return chunks
}

func TestTranscriptBlockEncodersAreReusedWithoutMixingData(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 25; round++ {
				tag := fmt.Sprintf("w%d-r%d", worker, round)
				want := codecChunks(tag, 20)
				blocks, err := encodeTranscriptChunks(want)
				if err != nil || len(blocks) != 1 {
					t.Errorf("%s: encode = %d blocks, %v", tag, len(blocks), err)
					return
				}
				got, err := decodeTranscriptBlock(blocks[0])
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) != len(want) {
					t.Errorf("%s: decoded %d chunks, want %d", tag, len(got), len(want))
					return
				}
				for index := range want {
					if got[index].Seq != want[index].Seq || !bytes.Equal(got[index].Data, want[index].Data) || got[index].TabID != want[index].TabID {
						t.Errorf("%s: chunk %d differs after a reused encoder: %+v", tag, index, got[index])
						return
					}
				}
			}
		}(worker)
	}
	wg.Wait()
}

// Building a zstd encoder per block allocated over a megabyte each time; reuse keeps a flush cheap.
func TestTranscriptBlockEncodingDoesNotAllocateAnEncoderPerBlock(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes sync.Pool drop items at random, so allocation volume is not meaningful")
	}
	chunks := codecChunks("alloc", 10)
	if _, err := encodeTranscriptBlock(chunks); err != nil { // warm the pool
		t.Fatal(err)
	}
	const rounds = 50
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for round := 0; round < rounds; round++ {
		if _, err := encodeTranscriptBlock(chunks); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if perBlock := (after.TotalAlloc - before.TotalAlloc) / rounds; perBlock > 256<<10 {
		t.Fatalf("encoding a small block allocated %d bytes on average; the encoder must be reused", perBlock)
	}
}
