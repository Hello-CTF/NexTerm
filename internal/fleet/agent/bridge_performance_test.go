package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const benchmarkSupervisorFrameSize = 5 + 8 + 32*1024

type benchmarkFramePipe struct {
	frame  []byte
	offset int
}

func (p *benchmarkFramePipe) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	count := copy(buffer, p.frame[p.offset:])
	p.offset += count
	if p.offset == len(p.frame) {
		p.offset = 0
	}
	return count, nil
}

func (p *benchmarkFramePipe) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (p *benchmarkFramePipe) Close() error                     { return nil }

func TestBridgePipeKeepsSupervisorFrameInOneMessage(t *testing.T) {
	messages, totalBytes := runBridgePipeSupervisorFrames(t, 1, nil)
	if messages != 1 || totalBytes != benchmarkSupervisorFrameSize {
		t.Fatalf("forwarded frame = %d messages/%d bytes, want 1/%d", messages, totalBytes, benchmarkSupervisorFrameSize)
	}
}

func BenchmarkBridgePipeSupervisorFrames(b *testing.B) {
	messages, totalBytes := runBridgePipeSupervisorFrames(b, b.N, b)
	b.ReportMetric(float64(messages)/float64(b.N), "messages/frame")
	b.ReportMetric(float64(totalBytes)/float64(b.N), "bytes/frame")
}

func runBridgePipeSupervisorFrames(tb testing.TB, frames int, benchmark *testing.B) (int, int) {
	tb.Helper()
	frame := bytes.Repeat([]byte("x"), benchmarkSupervisorFrameSize)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	defer func() {
		close(handlerDone)
		server.Close()
	}()
	agentConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		tb.Fatal(err)
	}
	agentConn.SetReadLimit(1 << 20)
	serverConn := <-accepted
	pipe := &benchmarkFramePipe{frame: frame}
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		_ = BridgePipe(ctx, agentConn, func(context.Context, string) (io.ReadWriteCloser, error) {
			return pipe, nil
		}, "")
	}()

	targetBytes := frames * len(frame)
	messages := 0
	totalBytes := 0
	readCtx, readCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readCancel()
	if benchmark != nil {
		benchmark.ReportAllocs()
		benchmark.ResetTimer()
	}
	for totalBytes < targetBytes {
		messageType, payload, err := serverConn.Read(readCtx)
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
	if totalBytes != targetBytes {
		tb.Fatalf("forwarded bytes = %d, want %d", totalBytes, targetBytes)
	}
	_ = agentConn.CloseNow()
	_ = serverConn.CloseNow()
	cancel()
	select {
	case <-bridgeDone:
	case <-time.After(5 * time.Second):
		tb.Fatal("BridgePipe did not stop")
	}
	return messages, totalBytes
}
