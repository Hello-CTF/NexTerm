//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fakeUnaryServer runs the hello handshake, then hands each incoming frame to
// handle until handle returns false. Unlike fakeStreamServer it performs no
// attach handshake, so unary requests can be exercised.
func fakeUnaryServer(t *testing.T, stateDir string, handle func(conn net.Conn, kind frameType, payload []byte) bool) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-unary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		kind, payload, err := readFrame(conn)
		if err != nil || kind != frameHello {
			return
		}
		var hello helloMsg
		if err := unmarshalFrame(payload, &hello); err != nil {
			return
		}
		if hello.Version != ProtocolVersion || hello.StateDigest != digest {
			_ = writeFrame(conn, frameError, []byte(`{"code":"state_mismatch","message":"wrong state"}`))
			return
		}
		if err := writeFrame(conn, frameHelloAck, fakeHelloAck()); err != nil {
			return
		}
		for {
			kind, payload, err = readFrame(conn)
			if err != nil {
				return
			}
			if !handle(conn, kind, payload) {
				return
			}
		}
	}()
	return path
}

// ackInput is a fakeStreamServer handle that acknowledges every input frame.
func ackInput(conn net.Conn, kind frameType, payload []byte) bool {
	if kind == frameInput {
		ack, err := marshalFrame(frameInputAck, inputAckMsg{Written: len(payload)})
		if err != nil {
			return false
		}
		if err := writeFrame(conn, frameInputAck, ack); err != nil {
			return false
		}
	}
	return true
}

func TestClientConnRequestRawPreCancelledSendsNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var mu sync.Mutex
	frames := 0
	path := fakeUnaryServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		mu.Lock()
		frames++
		mu.Unlock()
		if kind == frameList {
			_ = writeFrame(conn, frameListed, []byte("[]"))
		}
		return true
	})
	client := NewClient(path, stateDir)
	conn, err := client.dialConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := conn.request(ctx, frameList, struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled request = %v; want context.Canceled", err)
	}
	kind, _, err := conn.request(context.Background(), frameList, struct{}{})
	if err != nil || kind != frameListed {
		t.Fatalf("live request after a pre-cancelled one = %v, kind %d; want frameListed", err, kind)
	}
	mu.Lock()
	defer mu.Unlock()
	if frames != 1 {
		t.Fatalf("server saw %d frames; want only the live one", frames)
	}
}

func TestClientConnRequestRawCancelClosesConn(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	gotRequest := make(chan struct{})
	sawClose := make(chan struct{})
	path := fakeUnaryServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		close(gotRequest)
		// Stall without replying; the next read observes the client close.
		if _, _, err := readFrame(conn); err != nil {
			close(sawClose)
		}
		return false
	})
	client := NewClient(path, stateDir)
	conn, err := client.dialConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := conn.request(ctx, frameList, struct{}{})
		errCh <- err
	}()
	select {
	case <-gotRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the request")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request never returned")
	}
	select {
	case <-sawClose:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request left the conn open; the reply reader stays parked")
	}
}

func TestStreamWriteContextCancelFailsStreamExactlyOnce(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	writeStarted := make(chan struct{})
	releaseAck := make(chan struct{})
	var mu sync.Mutex
	inputs := 0
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind != frameInput {
			return true
		}
		mu.Lock()
		inputs++
		mu.Unlock()
		select {
		case <-writeStarted:
		default:
			close(writeStarted)
		}
		<-releaseAck
		// The late acknowledgement races the cancellation: it must never be
		// consumed by a later request.
		ack, err := marshalFrame(frameInputAck, inputAckMsg{Written: len(payload)})
		if err == nil {
			_ = writeFrame(conn, frameInputAck, ack)
		}
		return true
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.WriteContext(ctx, []byte("stalled"))
		writeErr <- err
	}()
	select {
	case <-writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the Write")
	}
	cancel()
	select {
	case err := <-writeErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteContext = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteContext never returned after cancel")
	}
	close(releaseAck)
	// The failed stream latches the cancellation: every later operation
	// completes exactly once with the same evidence and nothing new reaches
	// the wire, so the late ack cannot mismatch a later response.
	if _, err := stream.WriteContext(context.Background(), []byte("again")); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Write = %v; want the latched context.Canceled", err)
	}
	if err := stream.Resize(context.Background(), 100, 30); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resize = %v; want the latched context.Canceled", err)
	}
	if _, err := stream.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read = %v; want the latched context.Canceled", err)
	}
	if err := stream.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v; want the latched context.Canceled", err)
	}
	if err := stream.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close = %v; want the cancellation evidence, not success", err)
	}
	if err := stream.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Close = %v; want the same close evidence", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if inputs != 1 {
		t.Fatalf("server saw %d input frames; want exactly the cancelled one", inputs)
	}
}

func TestStreamWriteContextTimeout(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	writeStarted := make(chan struct{})
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind == frameInput {
			select {
			case <-writeStarted:
			default:
				close(writeStarted)
			}
		}
		return true
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = stream.WriteContext(ctx, []byte("stalled"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WriteContext = %v; want context.DeadlineExceeded", err)
	}
	select {
	case <-writeStarted:
	default:
		t.Fatal("server never saw the input frame")
	}
	if err := stream.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v; want the timeout evidence, not success", err)
	}
}

func TestStreamWriteContextPreCancelledKeepsStream(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var mu sync.Mutex
	inputs := 0
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind == frameInput {
			mu.Lock()
			inputs++
			mu.Unlock()
		}
		return ackInput(conn, kind, payload)
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	written, err := stream.WriteContext(ctx, []byte("withdrawn"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled WriteContext = %v; want context.Canceled", err)
	}
	if written != 0 {
		t.Fatalf("pre-cancelled WriteContext reported %d written; want 0", written)
	}
	count, err := stream.Write([]byte("confirmed"))
	if err != nil || count != len("confirmed") {
		t.Fatalf("Write after a pre-cancelled call = %d, %v; want a healthy stream", count, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if inputs != 1 {
		t.Fatalf("server saw %d input frames; the withdrawn request must not reach the wire", inputs)
	}
}

func TestStreamRequestCancelWhileQueuedSendsNothing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	inputs := 0
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind != frameInput {
			return true
		}
		mu.Lock()
		inputs++
		current := inputs
		mu.Unlock()
		if current == 1 {
			select {
			case <-firstStarted:
			default:
				close(firstStarted)
			}
			<-releaseFirst
		}
		return ackInput(conn, kind, payload)
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	firstErr := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("first"))
		firstErr <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the first Write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Deterministic entry into the token wait: the hook runs after the
	// initial ctx check and immediately before the token select, while the
	// first Write still holds the token.
	enteredWait := make(chan struct{})
	var waitOnce sync.Once
	stream.tokenWaitHook = func() { waitOnce.Do(func() { close(enteredWait) }) }
	queuedErr := make(chan error, 1)
	go func() {
		_, err := stream.WriteContext(ctx, []byte("queued"))
		queuedErr <- err
	}()
	select {
	case <-enteredWait:
	case <-time.After(5 * time.Second):
		t.Fatal("queued WriteContext never reached the token wait")
	}
	cancel()
	select {
	case err := <-queuedErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued WriteContext = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued WriteContext never returned after cancel")
	}
	// The queued request returned before releaseFirst: it woke on the ctx
	// while the token was still held, so its frame never reached the wire.
	close(releaseFirst)
	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first Write lost its reply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Write never completed")
	}
	if count, err := stream.Write([]byte("third")); err != nil || count != len("third") {
		t.Fatalf("Write after the cancelled queued request = %d, %v; want a healthy stream", count, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if inputs != 2 {
		t.Fatalf("server saw %d input frames; want the first and third only", inputs)
	}
}

func TestStreamCloseSurfacesDisconnectEvidence(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		// Drop the conn without acknowledging the input: the supervisor is
		// gone, not exited.
		return kind != frameInput
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("into the void")); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("Write = %v; want base.ErrDisconnected", err)
	}
	if err := stream.Close(); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("Close = %v; want the disconnect evidence, not success", err)
	}
	if err := stream.Close(); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("second Close = %v; want the same close evidence", err)
	}
}

func TestStreamCloseGracefulAfterExit(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	created, err := client.Create(ctx, CreateOptions{Command: []string{"/bin/sh", "-c", "true"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := stream.Wait(waitCtx); err != nil {
		t.Fatalf("Wait = %v; want a clean exit", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close after a clean exit = %v; want nil", err)
	}
}
