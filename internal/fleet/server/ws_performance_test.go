package fleetserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func benchmarkWSPair(tb testing.TB) (*websocket.Conn, *websocket.Conn) {
	tb.Helper()
	accepted := make(chan *websocket.Conn, 1)
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(1 << 20)
		accepted <- conn
		<-handlerDone
		_ = conn.CloseNow()
	}))
	peer, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		close(handlerDone)
		server.Close()
		tb.Fatal(err)
	}
	peer.SetReadLimit(1 << 20)
	serverConn := <-accepted
	tb.Cleanup(func() {
		_ = peer.CloseNow()
		_ = serverConn.CloseNow()
		close(handlerDone)
		server.Close()
	})
	return peer, serverConn
}

func TestPipeBridgeKeepsSupervisorFrameInOneMessage(t *testing.T) {
	messages, totalBytes := runPipeBridgeSupervisorFrames(t, 1, nil)
	const frameSize = 5 + 8 + 32*1024
	if messages != 1 || totalBytes != frameSize {
		t.Fatalf("forwarded frame = %d messages/%d bytes, want 1/%d", messages, totalBytes, frameSize)
	}
}

func BenchmarkPipeBridgeSupervisorFrames(b *testing.B) {
	messages, totalBytes := runPipeBridgeSupervisorFrames(b, b.N, b)
	b.ReportMetric(float64(messages)/float64(b.N), "messages/frame")
	b.ReportMetric(float64(totalBytes)/float64(b.N), "bytes/frame")
}

func runPipeBridgeSupervisorFrames(tb testing.TB, frames int, benchmark *testing.B) (int, int) {
	tb.Helper()
	const frameSize = 5 + 8 + 32*1024
	frame := bytes.Repeat([]byte("x"), frameSize)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userPeer, userServer := benchmarkWSPair(tb)
	agentPeer, agentServer := benchmarkWSPair(tb)
	bridge := newWSConn(agentServer)
	pipeDone := make(chan struct{})
	go func() {
		defer close(pipeDone)
		pipeBridge(userServer, bridge)
	}()
	writeErr := make(chan error, 1)
	targetBytes := frames * len(frame)
	messages := 0
	totalBytes := 0
	if benchmark != nil {
		benchmark.ReportAllocs()
		benchmark.ResetTimer()
	}
	go func() {
		for index := 0; index < frames; index++ {
			if err := agentPeer.Write(ctx, websocket.MessageBinary, frame); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()
	readCtx, readCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readCancel()
	for totalBytes < targetBytes {
		messageType, payload, err := userPeer.Read(readCtx)
		if err != nil {
			tb.Fatal(err)
		}
		if messageType != websocket.MessageBinary {
			tb.Fatalf("message type = %v, want binary", messageType)
		}
		messages++
		totalBytes += len(payload)
	}
	if benchmark != nil {
		benchmark.StopTimer()
	}
	if err := <-writeErr; err != nil {
		tb.Fatal(err)
	}
	if totalBytes != targetBytes {
		tb.Fatalf("forwarded bytes = %d, want %d", totalBytes, targetBytes)
	}
	_ = agentPeer.CloseNow()
	_ = userPeer.CloseNow()
	_ = bridge.Close()
	cancel()
	select {
	case <-pipeDone:
	case <-time.After(5 * time.Second):
		tb.Fatal("pipeBridge did not stop")
	}
	return messages, totalBytes
}
