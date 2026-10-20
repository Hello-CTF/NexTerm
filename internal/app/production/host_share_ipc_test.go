package production

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/coder/websocket"
)

type hostShareTestStatus struct {
	mu       sync.Mutex
	identity syncservice.RemoteHostShareIdentity
	err      error
}

func (s *hostShareTestStatus) HostShareIdentity(context.Context) (*syncservice.RemoteHostShareIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	identity := s.identity
	return &identity, nil
}

func (s *hostShareTestStatus) set(identity syncservice.RemoteHostShareIdentity) {
	s.mu.Lock()
	s.identity = identity
	s.err = nil
	s.mu.Unlock()
}

func (s *hostShareTestStatus) setError(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

type hostShareTestStream struct {
	frames   chan json.RawMessage
	closed   chan struct{}
	once     sync.Once
	isClosed atomic.Bool
}

func newHostShareTestStream() *hostShareTestStream {
	return &hostShareTestStream{frames: make(chan json.RawMessage, 32), closed: make(chan struct{})}
}

func (s *hostShareTestStream) SendJSON(ctx context.Context, payload json.RawMessage) error {
	if s.isClosed.Load() {
		return errors.New("stream closed")
	}
	copyOfPayload := append(json.RawMessage(nil), payload...)
	select {
	case s.frames <- copyOfPayload:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *hostShareTestStream) Close() error {
	s.once.Do(func() {
		s.isClosed.Store(true)
		close(s.closed)
	})
	return nil
}

type hostShareTestRead struct {
	kind    websocket.MessageType
	payload []byte
	err     error
}

type hostShareTestConn struct {
	reads      chan hostShareTestRead
	writes     chan []byte
	closeCodes chan websocket.StatusCode
	closed     chan struct{}
	closeOnce  sync.Once
	writeErr   error
}

func newHostShareTestConn() *hostShareTestConn {
	return &hostShareTestConn{
		reads: make(chan hostShareTestRead, 32), writes: make(chan []byte, 32),
		closeCodes: make(chan websocket.StatusCode, 1), closed: make(chan struct{}),
	}
}

func (c *hostShareTestConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case next := <-c.reads:
		return next.kind, next.payload, next.err
	case <-c.closed:
		return 0, nil, errors.New("remote connection closed")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *hostShareTestConn) Write(ctx context.Context, _ websocket.MessageType, payload []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	select {
	case c.writes <- append([]byte(nil), payload...):
		return nil
	case <-c.closed:
		return errors.New("remote connection closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *hostShareTestConn) Close(code websocket.StatusCode, _ string) error {
	select {
	case c.closeCodes <- code:
	default:
	}
	return c.CloseNow()
}

func (c *hostShareTestConn) CloseNow() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func newHostShareTestProxy(t *testing.T) (*hostShareIPC, *hostShareTestStatus, *hostShareTestConn, *hostShareTestStream) {
	t.Helper()
	status := &hostShareTestStatus{identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-a", Generation: "session-a"}}
	conn := newHostShareTestConn()
	stream := newHostShareTestStream()
	manager := newHostShareIPC(nil)
	manager.identity = status.HostShareIdentity
	manager.checkInterval = 10 * time.Millisecond
	manager.dial = func(context.Context, string) (hostShareConn, error) { return conn, nil }
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := &ipc.Call{
		Channel: ipc.ChannelRef{ID: "stream-1"},
		Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) { return stream, nil }},
	}
	if err := manager.Open(context.Background(), call, "device-1", "stream-1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return manager, status, conn, stream
}

func nextHostShareFrame(t *testing.T, stream *hostShareTestStream) json.RawMessage {
	t.Helper()
	select {
	case frame := <-stream.frames:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for host share stream frame")
		return nil
	}
}

func waitHostShareClosed(t *testing.T, closed <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func sendHostShareReady(conn *hostShareTestConn, permission string) {
	conn.reads <- hostShareTestRead{kind: websocket.MessageText, payload: []byte(`{"type":"ready","session_id":"session-1","permission":"` + permission + `","expires_at":1790000000000}`)}
}

func TestHostShareIPCReadOnlyOutputAndReadWriteInput(t *testing.T) {
	manager, _, conn, stream := newHostShareTestProxy(t)
	sendHostShareReady(conn, "read")
	if frame := nextHostShareFrame(t, stream); !json.Valid(frame) {
		t.Fatalf("ready frame = %q", frame)
	}
	if err := manager.Input("stream-1", []byte("not-allowed")); err == nil {
		t.Fatal("read-only input must be rejected")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
			t.Fatalf("read-only input error = %v", err)
		}
	}
	select {
	case input := <-conn.writes:
		t.Fatalf("read-only input reached remote: %q", input)
	default:
	}

	conn.reads <- hostShareTestRead{kind: websocket.MessageBinary, payload: []byte{0, 0xff, 'o', 'k'}}
	var output hostShareOutputFrame
	if err := json.Unmarshal(nextHostShareFrame(t, stream), &output); err != nil {
		t.Fatal(err)
	}
	if output.Type != "output" || string(output.Data) != string([]byte{0, 0xff, 'o', 'k'}) {
		t.Fatalf("output frame = %+v", output)
	}

	sendHostShareReady(conn, "read_write")
	_ = nextHostShareFrame(t, stream)
	if err := manager.Input("stream-1", []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-conn.writes:
		if string(input) != "ls\n" {
			t.Fatalf("remote input = %q", input)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote input")
	}

	sendHostShareReady(conn, "read")
	_ = nextHostShareFrame(t, stream)
	if err := manager.Input("stream-1", []byte("after-shrink")); err == nil {
		t.Fatal("updated ready must make the desktop proxy read-only")
	}

	if err := manager.Close("stream-1"); err != nil {
		t.Fatal(err)
	}
	waitHostShareClosed(t, conn.closed, "remote close")
	waitHostShareClosed(t, stream.closed, "local stream close")
	select {
	case code := <-conn.closeCodes:
		if code != websocket.StatusNormalClosure {
			t.Fatalf("close code = %v", code)
		}
	default:
		t.Fatal("viewer close did not reach remote")
	}
}

func TestHostShareIPCRevocationAndNormalClose(t *testing.T) {
	t.Run("revocation preserves server error", func(t *testing.T) {
		_, _, conn, stream := newHostShareTestProxy(t)
		conn.reads <- hostShareTestRead{kind: websocket.MessageText, payload: []byte(`{"type":"error","code":"forbidden","message":"主机分享已吊销"}`)}
		var failure hostShareErrorFrame
		if err := json.Unmarshal(nextHostShareFrame(t, stream), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.Code != "forbidden" || failure.Message != "主机分享已吊销" {
			t.Fatalf("error frame = %+v", failure)
		}
		conn.reads <- hostShareTestRead{err: websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "share terminated"}}
		var closed hostShareCloseFrame
		if err := json.Unmarshal(nextHostShareFrame(t, stream), &closed); err != nil {
			t.Fatal(err)
		}
		if closed.Type != "close" || closed.Code != int(websocket.StatusPolicyViolation) || closed.WasClean {
			t.Fatalf("close frame = %+v", closed)
		}
		waitHostShareClosed(t, stream.closed, "revoked local stream close")
	})

	t.Run("normal close stays clean", func(t *testing.T) {
		_, _, conn, stream := newHostShareTestProxy(t)
		conn.reads <- hostShareTestRead{err: websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: "share ended"}}
		var closed hostShareCloseFrame
		if err := json.Unmarshal(nextHostShareFrame(t, stream), &closed); err != nil {
			t.Fatal(err)
		}
		if closed.Code != int(websocket.StatusNormalClosure) || !closed.WasClean || closed.Reason != "share ended" {
			t.Fatalf("close frame = %+v", closed)
		}
	})
}

func TestHostShareIPCIdentityChangeClosesBothDirections(t *testing.T) {
	tests := []struct {
		name     string
		identity syncservice.RemoteHostShareIdentity
	}{
		{name: "user", identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-b", Generation: "session-a"}},
		{name: "server-with-same-user", identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://b.example.com", UserID: "user-a", Generation: "session-a"}},
		{name: "session-replacement", identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-a", Generation: "session-b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, status, conn, stream := newHostShareTestProxy(t)
			status.set(test.identity)
			var failure hostShareErrorFrame
			if err := json.Unmarshal(nextHostShareFrame(t, stream), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Code != "forbidden" {
				t.Fatalf("identity switch error = %+v", failure)
			}
			var closed hostShareCloseFrame
			if err := json.Unmarshal(nextHostShareFrame(t, stream), &closed); err != nil {
				t.Fatal(err)
			}
			waitHostShareClosed(t, conn.closed, "identity-switch remote close")
			waitHostShareClosed(t, stream.closed, "identity-switch local close")
		})
	}
}

func TestHostShareIPCLogoutClosesBothDirections(t *testing.T) {
	_, status, conn, stream := newHostShareTestProxy(t)
	status.setError(ipc.NewError(ipc.CodeForbidden, "logged out"))
	var failure hostShareErrorFrame
	if err := json.Unmarshal(nextHostShareFrame(t, stream), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "disconnected" {
		t.Fatalf("logout error = %+v", failure)
	}
	var closed hostShareCloseFrame
	if err := json.Unmarshal(nextHostShareFrame(t, stream), &closed); err != nil {
		t.Fatal(err)
	}
	waitHostShareClosed(t, conn.closed, "logout remote close")
	waitHostShareClosed(t, stream.closed, "logout local close")
}

func TestHostShareIPCWriteFailureClosesBothDirections(t *testing.T) {
	manager, _, conn, stream := newHostShareTestProxy(t)
	sendHostShareReady(conn, "read_write")
	_ = nextHostShareFrame(t, stream)
	conn.writeErr = errors.New("write failed")
	if err := manager.Input("stream-1", []byte("ls")); err == nil {
		t.Fatal("write failure must be returned")
	}
	var failure hostShareErrorFrame
	if err := json.Unmarshal(nextHostShareFrame(t, stream), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "disconnected" {
		t.Fatalf("write failure frame = %+v", failure)
	}
	_ = nextHostShareFrame(t, stream)
	waitHostShareClosed(t, conn.closed, "write-failure remote close")
	waitHostShareClosed(t, stream.closed, "write-failure local close")
}

func TestHostShareIPCSlowConsumerTimesOutAndCloses(t *testing.T) {
	manager, _, conn, stream := newHostShareTestProxy(t)
	manager.mu.Lock()
	manager.proxies["stream-1"].writeTimeout = 20 * time.Millisecond
	manager.mu.Unlock()
	for index := 0; index < cap(stream.frames)+1; index++ {
		conn.reads <- hostShareTestRead{kind: websocket.MessageBinary, payload: []byte("output")}
	}
	waitHostShareClosed(t, conn.closed, "slow-consumer remote close")
	waitHostShareClosed(t, stream.closed, "slow-consumer local close")
}

func TestHostShareIPCCloseWhileDialIsPending(t *testing.T) {
	status := &hostShareTestStatus{identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-a", Generation: "session-a"}}
	conn := newHostShareTestConn()
	stream := newHostShareTestStream()
	manager := newHostShareIPC(nil)
	manager.identity = status.HostShareIdentity
	started := make(chan struct{})
	release := make(chan struct{})
	manager.dial = func(context.Context, string) (hostShareConn, error) {
		close(started)
		<-release
		return conn, nil
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := &ipc.Call{
		Channel: ipc.ChannelRef{ID: "stream-1"},
		Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) { return stream, nil }},
	}
	openResult := make(chan error, 1)
	go func() { openResult <- manager.Open(context.Background(), call, "device-1", "stream-1") }()
	<-started
	if err := manager.Close("stream-1"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-openResult; err == nil {
		t.Fatal("open must fail after the viewer closes")
	}
	waitHostShareClosed(t, conn.closed, "pending-dial remote close")
	waitHostShareClosed(t, stream.closed, "pending-dial local close")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = manager.Shutdown(ctx)
}

func TestHostShareIPCDialIdentitySwitchSameUser(t *testing.T) {
	status := &hostShareTestStatus{identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-a", Generation: "session-a"}}
	conn := newHostShareTestConn()
	stream := newHostShareTestStream()
	manager := newHostShareIPC(nil)
	manager.identity = status.HostShareIdentity
	started := make(chan struct{})
	release := make(chan struct{})
	manager.dial = func(context.Context, string) (hostShareConn, error) {
		close(started)
		<-release
		status.set(syncservice.RemoteHostShareIdentity{ServerURL: "https://b.example.com", UserID: "user-a", Generation: "session-a"})
		return conn, nil
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	call := &ipc.Call{
		Channel: ipc.ChannelRef{ID: "stream-1"},
		Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) { return stream, nil }},
	}
	openResult := make(chan error, 1)
	go func() { openResult <- manager.Open(context.Background(), call, "device-1", "stream-1") }()
	<-started
	close(release)
	if err := <-openResult; err == nil {
		t.Fatal("dial must not survive a same-user server switch")
	}
	waitHostShareClosed(t, conn.closed, "identity-switch dial remote close")
	waitHostShareClosed(t, stream.closed, "identity-switch dial local close")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = manager.Shutdown(ctx)
}

func TestHostShareIPCDialReturnCloseAndShutdownRaces(t *testing.T) {
	for _, action := range []string{"close", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			for iteration := 0; iteration < 25; iteration++ {
				status := &hostShareTestStatus{identity: syncservice.RemoteHostShareIdentity{ServerURL: "https://a.example.com", UserID: "user-a", Generation: "session-a"}}
				conn := newHostShareTestConn()
				stream := newHostShareTestStream()
				manager := newHostShareIPC(nil)
				manager.identity = status.HostShareIdentity
				release := make(chan struct{})
				returned := make(chan struct{})
				manager.dial = func(context.Context, string) (hostShareConn, error) {
					<-release
					close(returned)
					return conn, nil
				}
				if err := manager.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				call := &ipc.Call{
					Channel: ipc.ChannelRef{ID: "stream-1"},
					Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) { return stream, nil }},
				}
				openResult := make(chan error, 1)
				go func() { openResult <- manager.Open(context.Background(), call, "device-1", "stream-1") }()
				close(release)
				<-returned
				if action == "close" {
					_ = manager.Close("stream-1")
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = manager.Shutdown(ctx)
					cancel()
				}
				select {
				case <-openResult:
				case <-time.After(2 * time.Second):
					t.Fatalf("iteration %d: Open did not finish after %s", iteration, action)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = manager.Shutdown(ctx)
				cancel()
				waitHostShareClosed(t, conn.closed, "dial-return remote close")
				waitHostShareClosed(t, stream.closed, "dial-return local close")
			}
		})
	}
}
