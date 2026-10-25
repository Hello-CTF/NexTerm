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

// TestCompatMatrixOutputZstd checks that compressed output is used only when
// both peers advertise it and that the stream content is identical either way.
func TestCompatMatrixOutputZstd(t *testing.T) {
	for _, m := range compatMatrix {
		t.Run(m.name, func(t *testing.T) {
			_, client, _ := matrixEnv(t, m)
			session := testScriptSessionForClient(t, client, zstdLogScript)
			if got := session.conn.supports(featureOutputZstd); got != m.extended() {
				t.Fatalf("output_zstd negotiated = %v, want %v", got, m.extended())
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
		})
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
		case frameInputAck:
			continue
		default:
			t.Fatalf("unexpected frame kind %d", kind)
		}
		kinds[kind]++
		wire += 5 + len(payload)
	}
	return decoded, kinds, wire
}

func rawAttach(t *testing.T, features []string, script string) (net.Conn, func()) {
	t.Helper()
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, script)
	conn := rawHello(t, supervisor, server, features)
	payload, _ := marshalFrame(frameAttach, attachMsg{ID: session.ID()})
	if err := writeFrame(conn, frameAttach, payload); err != nil {
		t.Fatal(err)
	}
	for {
		kind, _, err := readFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		if kind == frameAttached {
			break
		}
	}
	return conn, func() {}
}

func TestServerSendsZstdOnlyWhenNegotiated(t *testing.T) {
	conn, _ := rawAttach(t, []string{featureOutputZstd}, zstdLogScript)
	decoded, kinds, wire := readRawFrames(t, conn, "LOG-END")
	if kinds[frameOutputZstd] == 0 {
		t.Fatalf("no compressed frames with output_zstd negotiated: %v", kinds)
	}
	t.Logf("negotiated: %d decoded bytes in %d wire bytes (ratio %.3f), frames %v", len(decoded), wire, float64(wire)/float64(len(decoded)), kinds)

	conn, _ = rawAttach(t, nil, zstdLogScript)
	plain, kinds, _ := readRawFrames(t, conn, "LOG-END")
	if kinds[frameOutputZstd] != 0 {
		t.Fatalf("compressed frames without negotiation: %v", kinds)
	}
	if len(plain) == 0 {
		t.Fatal("no output")
	}
}

func TestServerKeepsSmallEchoFramesRaw(t *testing.T) {
	conn, _ := rawAttach(t, []string{featureOutputZstd}, "stty raw -echo; exec cat")
	for i := 0; i < 5; i++ {
		if err := writeFrame(conn, frameInput, []byte("echo-line-"+strconv.Itoa(i)+"\n")); err != nil {
			t.Fatal(err)
		}
		_, kinds, _ := readRawFrames(t, conn, "echo-line-"+strconv.Itoa(i))
		if kinds[frameOutputZstd] != 0 {
			t.Fatalf("small echo was compressed: %v", kinds)
		}
	}
}

// TestOutputCreditCountsDecodedBytesWithZstd withholds credit from a flood
// that compresses extremely well. The server must stop once the window of
// decoded bytes is in flight, not after the window's worth of wire bytes.
func TestOutputCreditCountsDecodedBytesWithZstd(t *testing.T) {
	const total = 16 << 20
	conn, _ := rawAttach(t, []string{featureOutputZstd, featureOutputCredit},
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
		case frameOutput:
			_, data, _ := parseOutput(payload)
			decoded += len(data)
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
