//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const zstdLogScript = `i=0; while [ $i -lt 600 ]; do printf '[ %3d%%] Building CXX object src/module/file%d.cc.o with flags -O2 -g -Wall\n' $((i/6)) $i; i=$((i+1)); done; printf 'LOG-END\n'; sleep 60`

// TestRemoteClientOutputZstd checks that a remote client receives compressed
// output and that the stream content survives it intact.
func TestRemoteClientOutputZstd(t *testing.T) {
	_, client, _ := flowEnv(t)
	session := testScriptSessionForClient(t, client, zstdLogScript)
	if !session.conn.compress {
		t.Fatal("remote client did not ask for compressed output")
	}
	var got bytes.Buffer
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(2 * testReadTimeout)
	for !bytes.Contains(got.Bytes(), []byte("LOG-END")) {
		if time.Now().After(deadline) {
			t.Fatal("log end not received")
		}
		n, err := session.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got.Write(buf[:n])
	}
	if !bytes.Contains(got.Bytes(), []byte("file599.cc.o")) || !bytes.Contains(got.Bytes(), []byte("file0.cc.o")) {
		t.Fatalf("stream lost content: %d bytes", got.Len())
	}
}

func testScriptSessionForClient(t *testing.T, client *Client, script string) *Stream {
	t.Helper()
	attachment, err := NewRemoteProvider(client).Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", script},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attachment.Kill(context.Background()) })
	return attachment.(*streamAttachment).Stream
}

// readRawFrames reads attach output frames from conn until marker is decoded.
// It returns the decoded stream and how many frames of each kind arrived.
func readRawFrames(t *testing.T, conn net.Conn, marker string) (decoded []byte, kinds map[frameType]int, wire int) {
	t.Helper()
	kinds = map[frameType]int{}
	_ = conn.SetReadDeadline(time.Now().Add(2 * testReadTimeout))
	for !bytes.Contains(decoded, []byte(marker)) {
		kind, payload, err := readFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch kind {
		case frameOutput:
			_, data, err := parseOutput(payload)
			if err != nil {
				t.Fatal(err)
			}
			decoded = append(decoded, data...)
		case frameOutputZstd:
			_, data, err := parseOutputZstd(payload)
			if err != nil {
				t.Fatal(err)
			}
			decoded = append(decoded, data...)
		case frameReplayGzip:
			// Output recorded before the attach arrives as compressed replay.
			_, data, err := parseReplayGzip(payload)
			if err != nil {
				t.Fatal(err)
			}
			decoded = append(decoded, data...)
		case frameInputAck, frameState:
			continue
		default:
			t.Fatalf("unexpected frame kind %d", kind)
		}
		kinds[kind]++
		wire += 5 + len(payload)
	}
	return decoded, kinds, wire
}

func rawAttach(t *testing.T, compress bool, script string) (net.Conn, Info) {
	t.Helper()
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, script)
	conn := rawHello(t, supervisor, server, compress)
	return conn, rawAttachSession(t, conn, attachMsg{ID: session.ID()})
}

func TestServerSendsZstdOnlyWhenRequested(t *testing.T) {
	conn, _ := rawAttach(t, true, zstdLogScript)
	decoded, kinds, wire := readRawFrames(t, conn, "LOG-END")
	if kinds[frameOutputZstd]+kinds[frameReplayGzip] == 0 {
		t.Fatalf("no compressed frames although the client asked for them: %v", kinds)
	}
	t.Logf("compressed: %d decoded bytes in %d wire bytes (ratio %.3f), frames %v", len(decoded), wire, float64(wire)/float64(len(decoded)), kinds)

	conn, _ = rawAttach(t, false, zstdLogScript)
	plain, kinds, _ := readRawFrames(t, conn, "LOG-END")
	if kinds[frameOutputZstd]+kinds[frameReplayGzip] != 0 {
		t.Fatalf("compressed frames the client did not ask for: %v", kinds)
	}
	if len(plain) == 0 {
		t.Fatal("no output")
	}
}

func TestServerKeepsSmallEchoFramesRaw(t *testing.T) {
	conn, info := rawAttach(t, true, "stty raw -echo; exec cat")
	for i := 0; i < 5; i++ {
		rawInput(t, conn, info.Generation, info.Incarnation, []byte("echo-line-"+strconv.Itoa(i)+"\n"))
		_, kinds, _ := readRawFrames(t, conn, "echo-line-"+strconv.Itoa(i))
		if kinds[frameOutputZstd]+kinds[frameReplayGzip] != 0 {
			t.Fatalf("small echo was compressed: %v", kinds)
		}
	}
}

// TestOutputCreditCountsDecodedBytesWithZstd withholds credit from a flood
// that compresses extremely well. The server must stop once the window of
// decoded bytes is in flight, not after the window's worth of wire bytes.
func TestOutputCreditCountsDecodedBytesWithZstd(t *testing.T) {
	const total = 16 << 20
	conn, _ := rawAttach(t, true,
		"head -c "+strconv.Itoa(total)+" /dev/zero | tr '\\000' x; printf 'FLOOD-END\\n'; sleep 60")
	var decoded, wire int
	compressed := 0
	var idle bool
	_ = conn.SetReadDeadline(time.Now().Add(2 * testReadTimeout))
	for !idle {
		if decoded >= outputCreditWindow {
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		}
		kind, payload, err := readFrame(conn)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() && decoded >= outputCreditWindow {
				idle = true
				continue
			}
			t.Fatalf("read frame: %v", err)
		}
		switch kind {
		case frameOutputZstd:
			compressed++
			_, data, err := parseOutputZstd(payload)
			if err != nil {
				t.Fatal(err)
			}
			decoded += len(data)
		case frameReplayGzip:
			compressed++
			_, data, err := parseReplayGzip(payload)
			if err != nil {
				t.Fatal(err)
			}
			decoded += len(data)
		case frameOutput:
			_, data, _ := parseOutput(payload)
			decoded += len(data)
		case frameState:
			continue
		default:
			t.Fatalf("unexpected frame kind %d", kind)
		}
		wire += 5 + len(payload)
	}
	if compressed == 0 {
		t.Fatal("flood was not compressed")
	}
	if decoded > outputCreditWindow+outputChunkSize {
		t.Fatalf("server streamed %d decoded bytes without credit, window %d", decoded, outputCreditWindow)
	}
	if wire >= decoded/10 {
		t.Fatalf("expected a strongly compressed flood, wire %d for %d decoded", wire, decoded)
	}
}
