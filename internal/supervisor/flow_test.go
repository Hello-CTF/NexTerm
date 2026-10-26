//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// flowEnv starts a server and a remote client for it. dials counts the
// connections the client opens, standing in for SSH exec channels.
func flowEnv(t *testing.T) (*Supervisor, *Client, *atomic.Int64) {
	t.Helper()
	supervisor := testSupervisor(t)
	runDir, err := os.MkdirTemp("/tmp", "nx-flow-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := NewServer(supervisor, filepath.Join(runDir, "supervisor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	dials := &atomic.Int64{}
	client := NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "unix", server.SocketPath())
	}, digest)
	return supervisor, client, dials
}

func catSession(t *testing.T, provider *RemoteProvider, id string) *streamAttachment {
	t.Helper()
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "stty raw -echo; exec cat"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return attachment.(*streamAttachment)
}

func TestCreateAttachUsesOneConnection(t *testing.T) {
	supervisor, client, dials := flowEnv(t)
	provider := NewRemoteProvider(client)
	attachment := catSession(t, provider, ids.New())
	defer func() { _ = attachment.Kill(context.Background()) }()
	if got := dials.Load(); got != 1 {
		t.Fatalf("Create opened %d connections, want 1", got)
	}
	if _, err := attachment.Write([]byte("chained-ping\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "chained-ping")
	infos, err := supervisor.List(context.Background())
	if err != nil || len(infos) != 1 {
		t.Fatalf("sessions = %v, %v, want exactly one", infos, err)
	}
}

func TestCreateAttachDuplicateIDKeepsExistingSession(t *testing.T) {
	supervisor, client, dials := flowEnv(t)
	provider := NewRemoteProvider(client)
	id := ids.New()
	first := catSession(t, provider, id)
	defer func() { _ = first.Kill(context.Background()) }()
	before := dials.Load()
	_, err := provider.Create(context.Background(), base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "exec cat"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err == nil {
		t.Fatal("duplicate Create succeeded")
	}
	if got := dials.Load() - before; got != 1 {
		t.Fatalf("rejected Create opened %d connections, want 1", got)
	}
	if _, err := first.Write([]byte("still-here\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, first, "still-here")
	if infos, _ := supervisor.List(context.Background()); len(infos) != 1 {
		t.Fatalf("sessions after rejected Create = %d, want 1", len(infos))
	}
}

func TestCreateAttachCompensatesWhenAttachFails(t *testing.T) {
	{
		supervisor, client, _ := flowEnv(t)
		provider := NewRemoteProvider(client)
		// The session exists but the stream is lost: Create must remove it.
		provider.createAttach = func(ctx context.Context, c *Client, options CreateOptions) (Info, *Stream, error) {
			info, stream, err := c.CreateAttach(ctx, options)
			if err != nil {
				return info, nil, err
			}
			_ = stream.Close()
			return info, nil, errors.New("attach response lost")
		}
		_, err := provider.Create(context.Background(), base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "exec cat"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		if err == nil {
			t.Fatal("Create must fail")
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil || len(infos) != 0 {
			t.Fatalf("compensation left sessions: %v, %v", infos, listErr)
		}
	}
}

func TestCreateAttachContextCanceledLeavesNoSession(t *testing.T) {
	supervisor, client, _ := flowEnv(t)
	provider := NewRemoteProvider(client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Create(ctx, base.DurableCreateOptions{Command: []string{"/bin/sh", "-c", "exec cat"}}); err == nil {
		t.Fatal("Create with a cancelled context succeeded")
	}
	if infos, _ := supervisor.List(context.Background()); len(infos) != 0 {
		t.Fatalf("cancelled Create left sessions: %+v", infos)
	}
}

// TestInputWindowBackpressure writes far more than the session queue and the
// PTY buffer hold while the program is not reading: the writes wait inside the
// input window instead of failing with a full input backlog.
func TestInputWindowBackpressure(t *testing.T) {
	const total = 6 << 20
	_, client, _ := flowEnv(t)
	runDir := t.TempDir()
	gate := filepath.Join(runDir, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := NewRemoteProvider(client)
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", "stty raw -echo; cat " + gate + " >/dev/null; head -c " + strconv.Itoa(total) + " | wc -c"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := attachment.(*streamAttachment).Stream
	done := make(chan error, 1)
	go func() {
		payload := bytes.Repeat([]byte("x"), total)
		n, err := stream.Write(payload)
		if err == nil && n != total {
			err = errors.New("short write")
		}
		if err == nil {
			err = stream.FlushInput(context.Background())
		}
		done <- err
	}()
	// Releasing the gate lets the program drain what was queued behind it.
	release, err := os.OpenFile(gate, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = release.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("windowed write failed: %v", err)
		}
	case <-time.After(testReadTimeout):
		t.Fatal("windowed write did not complete")
	}
	readUntil(t, stream, strconv.Itoa(total))
	_ = stream.Close()
}

// ackGate is a hand-driven supervisor that never acknowledges input until told.
type ackGate struct {
	frames chan int
	ack    chan *errorMsg
}

func fakeWindowServer(t *testing.T, gate *ackGate) *Client {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go func() {
		if _, _, err := readFrame(peer); err != nil {
			return
		}
		_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
		if _, _, err := readFrame(peer); err != nil {
			return
		}
		_ = writeFrame(peer, frameAttached, []byte(`{"id":"fake","incarnation":"i","cols":80,"rows":24}`))
		var pending []int
		inbox := make(chan [2]int, 64)
		go func() {
			for {
				kind, payload, err := readFrame(peer)
				if err != nil {
					close(inbox)
					return
				}
				if kind == frameInput {
					inbox <- [2]int{int(kind), len(inputData(payload))}
					continue
				}
				inbox <- [2]int{int(kind), 0}
			}
		}()
		for {
			select {
			case frame, ok := <-inbox:
				if !ok {
					return
				}
				if frameType(frame[0]) == frameInput {
					pending = append(pending, frame[1])
					gate.frames <- frame[1]
				}
				if frameType(frame[0]) == frameDetach {
					_ = writeFrame(peer, frameOK, []byte(`{}`))
				}
			case e := <-gate.ack:
				size := pending[0]
				pending = pending[1:]
				ack := inputAckMsg{Written: size, Error: e}
				payload, _ := marshalFrame(frameInputAck, ack)
				_ = writeFrame(peer, frameInputAck, payload)
			}
		}
	}()
	return NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
}

func TestInputWindowAcceptsWithoutAckAndBlocksAtLimit(t *testing.T) {
	gate := &ackGate{frames: make(chan int, 64), ack: make(chan *errorMsg, 64)}
	client := fakeWindowServer(t, gate)
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the window fits without a single acknowledgement.
	n, err := stream.WriteWindowed(context.Background(), make([]byte, inputWindowBytes))
	if err != nil || n != inputWindowBytes {
		t.Fatalf("Write within window = %d, %v", n, err)
	}
	waiting := make(chan struct{}, 1)
	stream.windowWaitHook = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := stream.WriteWindowed(ctx, []byte("over the window"))
		done <- struct {
			n   int
			err error
		}{n, err}
	}()
	<-waiting
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) || result.n != 0 {
		t.Fatalf("cancelled wait = %d, %v", result.n, result.err)
	}
	// The cancelled write sent nothing and the stream is still usable.
	sent := 0
	for sent < inputWindowBytes {
		sent += <-gate.frames
	}
	if sent != inputWindowBytes {
		t.Fatalf("supervisor saw %d input bytes, want %d", sent, inputWindowBytes)
	}
	gate.ack <- nil
	if n, err := stream.WriteWindowed(context.Background(), []byte("after")); err != nil || n != 5 {
		t.Fatalf("Write after cancelled wait = %d, %v", n, err)
	}
}

func TestInputWindowSurfacesEarlierRejectionLater(t *testing.T) {
	gate := &ackGate{frames: make(chan int, 64), ack: make(chan *errorMsg, 64)}
	client := fakeWindowServer(t, gate)
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := stream.WriteWindowed(context.Background(), []byte("first")); err != nil || n != 5 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	<-gate.frames
	gate.ack <- &errorMsg{Code: codeUnavailable, Message: "input rejected"}
	if err := stream.FlushInput(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("FlushInput = %v, want the rejection", err)
	}
	if n, err := stream.WriteWindowed(context.Background(), []byte("second")); !errors.Is(err, ErrUnavailable) || n != 0 {
		t.Fatalf("Write after rejection = %d, %v", n, err)
	}
	if err := stream.Close(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Close = %v, want the rejection", err)
	}
}

func TestInputWindowRejectsUnsolicitedAck(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go func() {
		_, _, _ = readFrame(peer)
		_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
		_, _, _ = readFrame(peer)
		_ = writeFrame(peer, frameAttached, []byte(`{"id":"fake","incarnation":"i"}`))
		_ = writeFrame(peer, frameInputAck, []byte(`{"written":3}`))
	}()
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Wait(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Wait = %v, want protocol error", err)
	}
}

func floodSession(t *testing.T, client *Client, total int) *Stream {
	t.Helper()
	provider := NewRemoteProvider(client)
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", "head -c " + strconv.Itoa(total) + " /dev/zero | tr '\\000' x; printf 'FLOOD-END\\n'; sleep 60"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attachment.Kill(context.Background()) })
	return attachment.(*streamAttachment).Stream
}

func waitBuffered(t *testing.T, stream *Stream, atLeast int) {
	t.Helper()
	deadline := time.Now().Add(testReadTimeout)
	for {
		stream.mu.Lock()
		have := stream.peakBuffered
		stream.mu.Unlock()
		if have >= atLeast {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog never reached %d bytes (peak %d)", atLeast, have)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func drainFlood(t *testing.T, stream *Stream, total int) {
	t.Helper()
	got := 0
	tail := make([]byte, 0, 16)
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(2 * testReadTimeout)
	for !bytes.Contains(tail, []byte("FLOOD-END")) {
		if time.Now().After(deadline) {
			t.Fatalf("flood not finished: %d bytes", got)
		}
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("flood read failed after %d bytes: %v", got, err)
		}
		got += n
		tail = append(tail, buf[:n]...)
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
		}
	}
	if want := total + len("FLOOD-END\r\n"); got < want-2 || got > want {
		t.Fatalf("read %d bytes, want %d", got, want)
	}
}

// TestOutputCreditSlowConsumerSurvivesWithBoundedBuffer lets the consumer fall
// behind an output flood far larger than the client's 64 MiB failure limit's
// share: the session must survive, requests must stay responsive, and the
// client backlog must stay within the credit window.
func TestOutputCreditSlowConsumerSurvivesWithBoundedBuffer(t *testing.T) {
	const total = 80 << 20
	_, client, _ := flowEnv(t)
	stream := floodSession(t, client, total)
	waitBuffered(t, stream, outputCreditWindow)
	// The consumer has not read anything; the control path must still answer.
	if _, _, err := stream.Versions(); err != nil {
		t.Fatalf("Versions while output is throttled: %v", err)
	}
	if err := stream.Resize(context.Background(), 100, 30); err != nil {
		t.Fatalf("Resize while output is throttled: %v", err)
	}
	drainFlood(t, stream, total)
	stream.mu.Lock()
	peak := stream.peakBuffered
	stream.mu.Unlock()
	if limit := outputCreditWindow + 2*replayChunkSize; peak > limit {
		t.Fatalf("peak backlog %d exceeds the credit window bound %d", peak, limit)
	}
}

func TestOutputCreditReplayAfterReattachIsAccounted(t *testing.T) {
	const total = 12 << 20
	_, client, _ := flowEnv(t)
	first := floodSession(t, client, total)
	waitBuffered(t, first, outputCreditWindow)
	id := first.Info().ID
	_ = first.Close()
	stream, err := client.AttachFrom(context.Background(), id, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	buf := make([]byte, 64<<10)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("replay read failed after %d bytes: %v", got, err)
		}
		got += n
		if bytes.Contains(buf[:n], []byte("FLOOD-END")) {
			break
		}
	}
	if want := total + len("FLOOD-END\r\n") - (1 << 20); got < want-2 {
		t.Fatalf("replay delivered %d bytes, want about %d", got, want)
	}
	_ = stream.Close()
}

func rawHello(t *testing.T, supervisor *Supervisor, server *Server, compress bool) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest, CompressOutput: compress})
	if err := writeFrame(conn, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameHelloAck {
		t.Fatalf("hello = %d, %v", kind, err)
	}
	return conn
}

func TestServerRejectsMalformedAndForgedCredit(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	credit := make([]byte, 8)
	binary.BigEndian.PutUint64(credit, 1)

	conn := rawHello(t, supervisor, server, false)
	if err := writeFrame(conn, frameCredit, credit[:4]); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameFatal {
		t.Fatalf("malformed credit answered with %d, %v, want fatal", kind, err)
	}

	conn = rawHello(t, supervisor, server, false)
	if err := writeFrame(conn, frameCredit, credit); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameFatal {
		t.Fatalf("credit beyond streamed output answered with %d, %v, want fatal", kind, err)
	}
}

// rawAttachSession sends an attach on a raw connection and returns the
// attached Info, skipping any output that precedes it.
func rawAttachSession(t *testing.T, conn net.Conn, msg attachMsg) Info {
	t.Helper()
	payload, err := marshalFrame(frameAttach, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameAttach, payload); err != nil {
		t.Fatal(err)
	}
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		if kind == frameAttached {
			var info Info
			if err := unmarshalFrame(payload, &info); err != nil {
				t.Fatal(err)
			}
			return info
		}
	}
}

// rawInput sends one frameInput carrying the given expectations.
func rawInput(t *testing.T, conn net.Conn, generation, incarnation string, data []byte) {
	t.Helper()
	payload, err := inputPayload(generation, incarnation, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameInput, payload); err != nil {
		t.Fatal(err)
	}
}

func TestServerWindowedInputAcksEachFrameOnce(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "stty raw -echo; exec cat >/dev/null")
	conn := rawHello(t, supervisor, server, false)
	info := rawAttachSession(t, conn, attachMsg{ID: session.ID()})
	const frames = 10
	for i := 0; i < frames; i++ {
		rawInput(t, conn, info.Generation, info.Incarnation, []byte("abc"))
	}
	acked := 0
	for acked < frames {
		kind, payload, err := readFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		if kind != frameInputAck {
			continue
		}
		var ack inputAckMsg
		if err := unmarshalFrame(payload, &ack); err != nil || ack.Written != 3 || ack.Error != nil {
			t.Fatalf("ack = %+v, %v", ack, err)
		}
		acked++
	}
}

func TestWindowedWriteContextWaitsForAcksAndReportsRejection(t *testing.T) {
	gate := &ackGate{frames: make(chan int, 64), ack: make(chan *errorMsg, 64)}
	client := fakeWindowServer(t, gate)
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := stream.WriteContext(context.Background(), make([]byte, 3*maxInputData))
		done <- struct {
			n   int
			err error
		}{n, err}
	}()
	// All three chunks are on the wire before any acknowledgement exists.
	for i := 0; i < 3; i++ {
		<-gate.frames
	}
	select {
	case <-done:
		t.Fatal("WriteContext returned before the acknowledgements")
	default:
	}
	gate.ack <- nil
	gate.ack <- &errorMsg{Code: codeUnavailable, Message: "rejected"}
	gate.ack <- nil
	result := <-done
	if !errors.Is(result.err, ErrUnavailable) || result.n != 2*maxInputData {
		t.Fatalf("WriteContext = %d, %v, want the rejection after %d acknowledged bytes", result.n, result.err, 2*maxInputData)
	}
}

func TestWindowedWriteContextCancelDuringAckWaitFailsStream(t *testing.T) {
	gate := &ackGate{frames: make(chan int, 64), ack: make(chan *errorMsg, 64)}
	client := fakeWindowServer(t, gate)
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := stream.WriteContext(ctx, []byte("unconfirmed"))
		done <- err
	}()
	<-gate.frames
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteContext = %v", err)
	}
	if _, err := stream.WriteContext(context.Background(), []byte("again")); !errors.Is(err, context.Canceled) {
		t.Fatalf("later write lost the cancellation evidence: %v", err)
	}
}

// A kill while the server is parked on exhausted credit must still deliver the
// exit.
func TestOutputCreditKillWhileParkedDeliversExit(t *testing.T) {
	supervisor, client, _ := flowEnv(t)
	stream := floodSession(t, client, 80<<20)
	waitBuffered(t, stream, outputCreditWindow)
	if err := supervisor.Kill(context.Background(), stream.Info().ID); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := stream.Wait(waitCtx); errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("exit was never delivered while credit was exhausted")
	}
	buf := make([]byte, 64<<10)
	for {
		if _, err := stream.Read(buf); err != nil {
			break
		}
	}
}

func TestWindowedWriteReturnsWhenSessionExitsAndConnectionCloses(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go func() {
		_, _, _ = readFrame(peer)
		_ = writeFrame(peer, frameHelloAck, fakeHelloAck())
		_, _, _ = readFrame(peer)
		_ = writeFrame(peer, frameAttached, []byte(`{"id":"fake","incarnation":"i"}`))
		for {
			kind, _, err := readFrame(peer)
			if err != nil {
				return
			}
			if kind == frameInput {
				_ = writeFrame(peer, frameExit, []byte(`{"code":0}`))
				_ = peer.Close()
				return
			}
		}
	}()
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return local, nil }, "digest")
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.WriteContext(context.Background(), []byte("input"))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("WriteContext reported success for unacknowledged input")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteContext did not return after the connection closed")
	}
}

func TestCloseReportsDroppedUnacknowledgedInput(t *testing.T) {
	gate := &ackGate{frames: make(chan int, 64), ack: make(chan *errorMsg, 64)}
	client := fakeWindowServer(t, gate)
	stream, err := client.Attach(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.closeDrainTimeout = 50 * time.Millisecond
	if _, err := stream.WriteWindowed(context.Background(), []byte("never acked")); err != nil {
		t.Fatal(err)
	}
	<-gate.frames
	err = stream.Close()
	if !errors.Is(err, base.ErrDisconnected) || err == nil || !bytes.Contains([]byte(err.Error()), []byte("1 unacknowledged input frames dropped")) {
		t.Fatalf("Close = %v, want the dropped-input evidence", err)
	}
}
