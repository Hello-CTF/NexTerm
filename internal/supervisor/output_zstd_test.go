package supervisor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
)

func buildLogLines(lines int) []byte {
	var out bytes.Buffer
	for i := 0; i < lines; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&out, "[%3d%%] Building CXX object src/module%d/file%d.cc.o\r\n", i*100/lines, i%37, i)
		case 1:
			fmt.Fprintf(&out, "/usr/bin/c++ -O2 -g -Wall -Wextra -std=c++20 -Iinclude -Isrc/module%d -c src/module%d/file%d.cc\r\n", i%37, i%37, i)
		case 2:
			fmt.Fprintf(&out, "warning: unused variable 'tmp%d' [-Wunused-variable] at file%d.cc:%d:%d\r\n", i, i, i%900, i%80)
		default:
			fmt.Fprintf(&out, "PASS  test_case_%d (%d ms)\r\n", i, i%53)
		}
	}
	return out.Bytes()
}

func TestOutputZstdRoundTrip(t *testing.T) {
	data := buildLogLines(2000)[:outputChunkSize]
	payload, err := encodeOutputZstd(1234, data)
	if err != nil || payload == nil {
		t.Fatalf("encode = %v, %v", payload != nil, err)
	}
	seq, got, err := parseOutputZstd(payload)
	if err != nil || seq != 1234 || !bytes.Equal(got, data) {
		t.Fatalf("round trip = seq %d, equal %v, err %v", seq, bytes.Equal(got, data), err)
	}
	if len(payload) >= 8+len(data) {
		t.Fatalf("payload %d not smaller than raw frame %d", len(payload), 8+len(data))
	}
}

func TestOutputZstdSmallAndIncompressibleStayRaw(t *testing.T) {
	if p, err := encodeOutputZstd(0, bytes.Repeat([]byte("a"), outputZstdMinBytes-1)); p != nil || err != nil {
		t.Fatalf("small chunk compressed: %v, %v", p != nil, err)
	}
	random := make([]byte, 4096)
	state := uint64(88172645463325252)
	for i := range random {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		random[i] = byte(state)
	}
	if p, err := encodeOutputZstd(0, random); p != nil || err != nil {
		t.Fatalf("incompressible chunk compressed: %v, %v", p != nil, err)
	}
	if p, err := encodeOutputZstd(0, bytes.Repeat([]byte("a"), outputZstdMinBytes)); p == nil || err != nil {
		t.Fatalf("chunk at threshold not compressed: %v, %v", p != nil, err)
	}
}

func TestOutputZstdRejectsBadPayloads(t *testing.T) {
	data := bytes.Repeat([]byte("hello world "), 100)
	good, err := encodeOutputZstd(7, data)
	if err != nil || good == nil {
		t.Fatal("encode failed")
	}
	withSize := func(size uint32) []byte {
		p := append([]byte(nil), good...)
		binary.BigEndian.PutUint32(p[8:], size)
		return p
	}
	corrupt := append([]byte(nil), good...)
	for i := outputZstdHeader; i < len(corrupt); i++ {
		corrupt[i] ^= 0xA5
	}
	// A bomb: tiny compressed input that expands far beyond the cap.
	encoder, _ := acquireOutputZstdEncoder()
	bomb := encoder.EncodeAll(make([]byte, 8<<20), make([]byte, outputZstdHeader))
	outputZstdEncoders.Put(encoder)
	binary.BigEndian.PutUint32(bomb[8:], 1024)

	cases := map[string][]byte{
		"empty":              nil,
		"header only":        good[:outputZstdHeader],
		"corrupt body":       corrupt,
		"truncated":          good[:len(good)-3],
		"declared zero":      withSize(0),
		"declared too large": withSize(outputZstdMaxDecoded + 1),
		"declared too small": withSize(uint32(len(data) - 1)),
		"declared too big":   withSize(uint32(len(data) + 1)),
		"expansion bomb":     bomb,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseOutputZstd(payload); !errors.Is(err, ErrProtocol) {
				t.Fatalf("parse = %v, want protocol error", err)
			}
		})
	}
}

func TestOutputZstdConcurrentCodecIsRaceFree(t *testing.T) {
	data := buildLogLines(64)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				chunk := data[:len(data)-i]
				payload, err := encodeOutputZstd(uint64(i), chunk)
				if err != nil || payload == nil {
					t.Errorf("encode: %v", err)
					return
				}
				_, got, err := parseOutputZstd(payload)
				if err != nil || !bytes.Equal(got, chunk) {
					t.Errorf("decode mismatch: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestOutputZstdClientRejectsBadFrames feeds a fake helper's invalid
// compressed frames to the client; each must surface as a protocol error.
func TestOutputZstdClientRejectsBadFrames(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefgh"), 100)
	good, _ := encodeOutputZstd(0, data)
	corrupt := append([]byte(nil), good...)
	corrupt[len(corrupt)-1] ^= 0xFF
	corrupt[outputZstdHeader] ^= 0xFF
	oversized := append([]byte(nil), good...)
	binary.BigEndian.PutUint32(oversized[8:], outputZstdMaxDecoded+1)

	cases := []struct {
		name     string
		compress bool
		payload  []byte
	}{
		{"corrupt", true, corrupt},
		{"oversized", true, oversized},
		{"not requested", false, good},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := net.Pipe()
			t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
			go func() {
				_, _, _ = readFrame(peer)
				_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
				_, _, _ = readFrame(peer)
				_ = writeFrame(peer, frameAttached, []byte(`{"id":"fake","incarnation":"i"}`))
				_ = writeFrame(peer, frameOutputZstd, tc.payload)
			}()
			client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
			client.compressOutput = tc.compress
			stream, err := client.Attach(context.Background(), "fake", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Wait(context.Background()); !errors.Is(err, ErrProtocol) {
				t.Fatalf("Wait = %v, want protocol error", err)
			}
		})
	}
}

// TestOutputZstdWireRatio reports wire versus raw bytes for a build-log-like
// stream, framed as the server would send it. It asserts correctness only.
func TestOutputZstdWireRatio(t *testing.T) {
	log := buildLogLines(20000)
	var raw, wire, compressedFrames, frames int
	var rebuilt bytes.Buffer
	var seq uint64
	for off := 0; off < len(log); off += outputChunkSize {
		chunk := log[off:min(off+outputChunkSize, len(log))]
		raw += len(chunk)
		payload, err := encodeOutputZstd(seq, chunk)
		if err != nil {
			t.Fatal(err)
		}
		frames++
		if payload == nil {
			wire += 5 + 8 + len(chunk)
			rebuilt.Write(chunk)
		} else {
			wire += 5 + len(payload)
			compressedFrames++
			_, data, err := parseOutputZstd(payload)
			if err != nil {
				t.Fatal(err)
			}
			rebuilt.Write(data)
		}
		seq += uint64(len(chunk))
	}
	if !bytes.Equal(rebuilt.Bytes(), log) {
		t.Fatal("stream did not round trip")
	}
	t.Logf("build-log stream (32 KiB frames): raw %d B, wire %d B, ratio %.3f (%d/%d frames compressed)",
		raw, wire, float64(wire)/float64(raw), compressedFrames, frames)

	// Typical PTY reads are far smaller than a full chunk; measure 1 KiB reads too.
	raw, wire = 0, 0
	for off := 0; off < len(log); off += 1024 {
		chunk := log[off:min(off+1024, len(log))]
		raw += len(chunk)
		payload, _ := encodeOutputZstd(0, chunk)
		if payload == nil {
			wire += 5 + 8 + len(chunk)
		} else {
			wire += 5 + len(payload)
		}
	}
	t.Logf("build-log stream (1 KiB frames): raw %d B, wire %d B, ratio %.3f", raw, wire, float64(wire)/float64(raw))
}
