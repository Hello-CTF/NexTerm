package supervisor

import (
	"bytes"
	"errors"
	"testing"
)

func TestReplayCompressionRoundTrip(t *testing.T) {
	var compressor replayCompressor
	data := bytes.Repeat([]byte("2026-10-18 terminal history line with repeated content\r\n"), 2000)
	for _, seq := range []uint64{0, 123456} {
		payload, err := compressor.encode(seq, data)
		if err != nil {
			t.Fatal(err)
		}
		gotSeq, got, err := parseReplayGzip(payload)
		if err != nil || gotSeq != seq || !bytes.Equal(got, data) {
			t.Fatalf("compressed replay round trip: seq=%d, length=%d, error=%v", gotSeq, len(got), err)
		}
		if len(payload) >= len(data)/10 {
			t.Fatalf("repetitive history compressed to %d of %d bytes", len(payload), len(data))
		}
		t.Logf("history: raw=%d gzip=%d", len(data), len(payload)-8)
	}
}

func TestReplayCompressionRejectsInvalidPayload(t *testing.T) {
	var compressor replayCompressor
	oversize, err := compressor.encode(0, bytes.Repeat([]byte("x"), replayChunkSize+1))
	if err != nil {
		t.Fatal(err)
	}
	oversize = bytes.Clone(oversize)
	corrupt, err := compressor.encode(0, []byte("history"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt = bytes.Clone(corrupt)
	corrupt[len(corrupt)-1] ^= 0xff
	for name, payload := range map[string][]byte{
		"short":        nil,
		"invalid gzip": outputPayload(0, []byte("not gzip")),
		"checksum":     corrupt,
		"oversize":     oversize,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseReplayGzip(payload); !errors.Is(err, ErrProtocol) {
				t.Fatalf("parse replay = %v, want ErrProtocol", err)
			}
		})
	}
}
