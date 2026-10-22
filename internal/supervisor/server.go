package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"
)

type Server struct {
	supervisor      *Supervisor
	stateDigest     string
	runtimeMetadata *RuntimeMetadata
	endpoint        endpointIdentity
	listener        net.Listener
	unlock          func()

	mu     sync.Mutex
	conns  map[*serverConn]struct{}
	closed bool

	wg sync.WaitGroup

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func NewServer(supervisor *Supervisor, socketPath string) (*Server, error) {
	if supervisor == nil {
		return nil, fmt.Errorf("%w: supervisor is required", ErrInvalidInput)
	}
	if socketPath == "" {
		return nil, fmt.Errorf("%w: socket path is required", ErrInvalidInput)
	}
	stateDigest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		return nil, err
	}
	listener, endpoint, unlock, err := prepareEndpoint(socketPath)
	if err != nil {
		return nil, err
	}
	server := &Server{
		supervisor:      supervisor,
		stateDigest:     stateDigest,
		runtimeMetadata: provenRuntimeMetadata(),
		endpoint:        endpoint,
		listener:        listener,
		unlock:          unlock,
		conns:           make(map[*serverConn]struct{}),
		shutdownDone:    make(chan struct{}),
	}
	server.wg.Add(1)
	go server.acceptLoop()
	return server, nil
}

func (s *Server) SocketPath() string {
	return s.endpoint.Path()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		listenErr := s.listener.Close()
		go s.finishShutdown(listenErr)
	})
	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Server) finishShutdown(listenErr error) {
	s.mu.Lock()
	conns := make([]*serverConn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		conn.close()
	}
	s.wg.Wait()
	if err := s.endpoint.Cleanup(); err != nil {
		listenErr = errors.Join(listenErr, err)
	}
	s.unlock()
	if errors.Is(listenErr, net.ErrClosed) {
		listenErr = nil
	}
	s.shutdownErr = listenErr
	close(s.shutdownDone)
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		serverConn := &serverConn{server: s, conn: conn}
		s.conns[serverConn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer s.removeConn(serverConn)
			serverConn.serve()
		}()
	}
}

func (s *Server) removeConn(conn *serverConn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

type serverConn struct {
	server *Server
	conn   net.Conn

	writeMu   sync.Mutex
	closeOnce sync.Once

	sessionMu  sync.Mutex
	attachment *Attachment

	clientFeatures []string

	// exec is the one-shot non-PTY session created on this connection; its
	// output streams here and it is killed when the connection drops.
	exec *Session
}

func (c *serverConn) serve() {
	defer c.close()
	defer func() {
		if recovered := recover(); recovered != nil {
			c.replyError(frameError, errorMsg{Code: codeInternal, Message: "internal supervisor error"})
		}
	}()
	if err := authorizePeer(c.conn); err != nil {
		c.reply(frameError, errorMsg{Code: codeProtocol, Message: "peer is not authorized"})
		return
	}
	kind, payload, err := readFrame(c.conn)
	if err != nil {
		return
	}
	if kind != frameHello {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "expected hello frame"})
		return
	}
	var hello helloMsg
	if err := unmarshalFrame(payload, &hello); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if hello.Version != ProtocolVersion {
		c.replyError(frameError, errorMsg{Code: codeVersionMismatch, Message: fmt.Sprintf("protocol version %d is not supported", hello.Version)})
		return
	}
	if hello.StateDigest != c.server.stateDigest {
		c.replyError(frameError, errorMsg{Code: codeStateMismatch, Message: "supervisor state digest does not match this endpoint"})
		return
	}
	c.clientFeatures = hello.Features
	ack := helloMsg{Version: ProtocolVersion, Features: HelperFeatures()}
	if slices.Contains(c.clientFeatures, featureRuntimeMetadata) {
		ack.Runtime = c.server.runtimeMetadata
	}
	if slices.Contains(c.clientFeatures, featureActivity) {
		activity, err := c.server.supervisor.Activity(context.Background())
		if err != nil {
			c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
			return
		}
		ack.Activity = &activity
	}
	if err := c.reply(frameHelloAck, ack); err != nil {
		return
	}
	for {
		kind, payload, err := readFrame(c.conn)
		if err != nil {
			return
		}
		switch kind {
		case frameCreate:
			c.handleCreate(payload)
		case frameList:
			c.handleList()
		case frameRename:
			c.handleRename(payload)
		case frameAttach:
			if !c.handleAttach(payload) {
				return
			}
		case frameInput:
			c.handleInput(payload)
		case frameResize:
			c.handleResize(payload)
		case frameKill:
			c.handleKill()
		case frameGetVersions:
			c.handleGetVersions()
		case frameSetVersions:
			c.handleSetVersions(payload)
		case frameKillSession:
			c.handleKillSession(payload)
		case frameDetach:
			_ = c.reply(frameOK, struct{}{})
			return
		default:
			c.replyError(frameError, errorMsg{Code: codeProtocol, Message: fmt.Sprintf("unexpected frame type %d", kind)})
			return
		}
	}
}

func (c *serverConn) handleCreate(payload []byte) {
	var msg createMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	usePTY := msg.Pty == nil || *msg.Pty
	if !usePTY && !slices.Contains(c.clientFeatures, featureExec) {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "client does not support non-PTY exec"})
		return
	}
	session, err := c.server.supervisor.Create(context.Background(), CreateOptions{
		ID:      msg.ID,
		Attempt: msg.Attempt,
		Command: msg.Command,
		Dir:     msg.Dir,
		Env:     msg.Env,
		Cols:    msg.Cols,
		Rows:    msg.Rows,
		Pty:     msg.Pty,
	})
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	if !usePTY {
		// Assign before replying: a concurrent close() that snapshots after this store kills the session.
		c.sessionMu.Lock()
		c.exec = session
		c.sessionMu.Unlock()
	}
	if err := c.reply(frameCreated, session.canonicalInfo()); err != nil {
		if !usePTY {
			// Without a stream consumer the pipes would never drain and execWait would never reap;
			// drain first so the kill below completes instead of leaking the session.
			session.drainExecPipes()
			identity := session.Identity()
			_ = c.server.supervisor.kill(context.Background(), session.ID(), &identity)
			c.sessionMu.Lock()
			c.exec = nil
			c.sessionMu.Unlock()
		}
		return
	}
	if !usePTY {
		go c.streamExec(session)
	}
}

func (c *serverConn) handleList() {
	infos, err := c.server.supervisor.listCanonical(context.Background())
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameListed, infos)
}

func (c *serverConn) handleRename(payload []byte) {
	if !slices.Contains(c.clientFeatures, featureMetadata) {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "client does not support session metadata"})
		return
	}
	var msg RenameRequest
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	info, err := c.server.supervisor.Rename(context.Background(), msg)
	if err != nil {
		c.replyOperationError(err)
		return
	}
	_ = c.reply(frameRenamed, info)
}

func (c *serverConn) attached() *Attachment {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	return c.attachment
}

func (c *serverConn) handleAttach(payload []byte) bool {
	if c.attached() != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "connection is already attached"})
		return true
	}
	var msg attachMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return true
	}
	var expected *Identity
	if msg.ExpectIncarnation != "" || msg.ExpectCreatedAt != 0 {
		expected = &Identity{CreatedAt: time.Unix(0, msg.ExpectCreatedAt), Incarnation: msg.ExpectIncarnation}
	}
	attachment, err := c.server.supervisor.attachFrom(context.Background(), msg.ID, msg.Offset, expected)
	if err != nil {
		c.replyOperationError(err)
		return true
	}
	c.sessionMu.Lock()
	c.attachment = attachment
	c.sessionMu.Unlock()
	if err := c.reply(frameAttached, attachment.session.canonicalInfo()); err != nil {
		_ = attachment.Detach()
		return false
	}
	go c.streamOutput(attachment)
	return true
}

func (c *serverConn) handleInput(payload []byte) {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "input frame without attach"})
		return
	}
	count, err := attachment.tryWrite(payload)
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameInputAck, inputAckMsg{Written: count})
}

func (c *serverConn) handleKillSession(payload []byte) {
	if c.attached() != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "kill session frame on attached connection"})
		return
	}
	var msg killSessionMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if msg.Attempt != "" {
		if err := c.server.supervisor.killByAttempt(context.Background(), msg.ID, msg.Attempt); err != nil {
			c.replyOperationError(err)
			return
		}
		_ = c.reply(frameOK, struct{}{})
		return
	}
	var expected *Identity
	if msg.ExpectIncarnation != "" || msg.ExpectCreatedAt != 0 {
		identity := Identity{CreatedAt: time.Unix(0, msg.ExpectCreatedAt), Incarnation: msg.ExpectIncarnation}
		expected = &identity
	}
	if err := c.server.supervisor.kill(context.Background(), msg.ID, expected); err != nil {
		c.replyOperationError(err)
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleResize(payload []byte) {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "resize frame without attach"})
		return
	}
	var msg resizeMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := attachment.Resize(context.Background(), msg.Cols, msg.Rows); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleKill() {
	c.sessionMu.Lock()
	session := c.exec
	c.sessionMu.Unlock()
	if session != nil {
		if err := c.execKill(context.Background(), session); err != nil {
			c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
			return
		}
		_ = c.reply(frameOK, struct{}{})
		return
	}
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "kill frame without attach"})
		return
	}
	if err := attachment.Kill(context.Background()); err != nil {
		c.replyOperationError(err)
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleGetVersions() {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "versions frame without attach"})
		return
	}
	event, grid, err := attachment.Versions()
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameVersions, versionsMsg{Event: event, Grid: grid})
}

func (c *serverConn) handleSetVersions(payload []byte) {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "versions frame without attach"})
		return
	}
	var msg versionsMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := attachment.PersistVersions(msg.Event, msg.Grid); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

// streamExec forwards the session's stdout and stderr as frameExecOutput
// frames and finishes with frameExit once the process is reaped. A write
// failure means the client is gone; the connection close then kills the
// process, which unblocks the remaining pipe reader.
func (c *serverConn) streamExec(session *Session) {
	var pumps sync.WaitGroup
	pumps.Add(2)
	go c.pumpExecPipe(session, session.exec.stdout, streamStdout, &pumps)
	go c.pumpExecPipe(session, session.exec.stderr, streamStderr, &pumps)
	pumps.Wait()
	<-session.deadCh
	info := session.Info()
	_ = c.reply(frameExit, exitMsg{Code: info.ExitCode, Signal: info.Signal})
}

func (c *serverConn) pumpExecPipe(session *Session, pipe io.Reader, stream byte, pumps *sync.WaitGroup) {
	defer pumps.Done()
	defer session.exec.drained.Done()
	buffer := make([]byte, outputChunkSize)
	for {
		count, err := pipe.Read(buffer)
		if count > 0 {
			if writeErr := c.replyRaw(frameExecOutput, execOutputPayload(stream, buffer[:count])); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// execKill terminates the one-shot session; an already finished session is
// dropped from the supervisor at exit, so ErrNotFound means the kill landed.
func (c *serverConn) execKill(ctx context.Context, session *Session) error {
	identity := session.Identity()
	err := c.server.supervisor.kill(ctx, session.ID(), &identity)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *serverConn) streamOutput(attachment *Attachment) {
	var replayEnd uint64
	if slices.Contains(c.clientFeatures, featureReplayGzip) {
		attachment.replayMu.Lock()
		if attachment.replay != nil {
			if info, err := attachment.replay.Stat(); err == nil {
				replayEnd = uint64(info.Size())
			}
		}
		attachment.replayMu.Unlock()
	}
	var compressor replayCompressor
	buffer := make([]byte, replayChunkSize)
	for {
		seq := attachment.NextSeq()
		size := outputChunkSize
		if seq < replayEnd {
			size = int(min(uint64(replayChunkSize), replayEnd-seq))
		}
		count, err := attachment.Read(buffer[:size])
		if count > 0 {
			kind, payload := frameOutput, outputPayload(seq, buffer[:count])
			if seq < replayEnd && count >= 1024 {
				if compressed, compressErr := compressor.encode(seq, buffer[:count]); compressErr == nil && len(compressed) < len(payload) {
					kind, payload = frameReplayGzip, compressed
				}
			}
			if writeErr := c.replyRaw(kind, payload); writeErr != nil {
				return
			}
			if seq+uint64(count) >= replayEnd {
				compressor = replayCompressor{}
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				info := attachment.Info()
				_ = c.reply(frameExit, exitMsg{Code: info.ExitCode, Signal: info.Signal})
			case errors.Is(err, ErrClosed):
				if info := attachment.Info(); info.Dead {
					_ = c.reply(frameExit, exitMsg{Code: info.ExitCode, Signal: info.Signal})
				}
			default:
				_ = c.reply(frameFatal, errorMsg{Code: errorCode(err), Message: err.Error()})
			}
			return
		}
	}
}

func (c *serverConn) reply(kind frameType, message any) error {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return err
	}
	return c.replyRaw(kind, payload)
}

func (c *serverConn) replyRaw(kind frameType, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.conn, kind, payload)
}

func (c *serverConn) replyError(kind frameType, msg errorMsg) {
	_ = c.reply(kind, msg)
}

func (c *serverConn) replyOperationError(err error) {
	msg := errorMessage(err)
	if errors.Is(err, ErrIdentity) && slices.Contains(c.clientFeatures, featureMetadata) {
		msg.Code = codeIdentityChanged
	}
	c.replyError(frameError, msg)
}

func (c *serverConn) close() {
	c.closeOnce.Do(func() {
		c.sessionMu.Lock()
		attachment := c.attachment
		session := c.exec
		c.sessionMu.Unlock()
		if attachment != nil {
			_ = attachment.Detach()
		}
		if session != nil {
			_ = c.execKill(context.Background(), session)
		}
		_ = c.conn.Close()
	})
}
