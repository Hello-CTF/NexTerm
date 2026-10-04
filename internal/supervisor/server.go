package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Server struct {
	supervisor  *Supervisor
	stateDigest string
	endpoint    endpointIdentity
	listener    net.Listener
	unlock      func()

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
		supervisor:   supervisor,
		stateDigest:  stateDigest,
		endpoint:     endpoint,
		listener:     listener,
		unlock:       unlock,
		conns:        make(map[*serverConn]struct{}),
		shutdownDone: make(chan struct{}),
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

	attachment *Attachment
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
	if err := c.reply(frameHelloAck, helloMsg{Version: ProtocolVersion}); err != nil {
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
	session, err := c.server.supervisor.Create(context.Background(), CreateOptions{
		ID:      msg.ID,
		Attempt: msg.Attempt,
		Command: msg.Command,
		Dir:     msg.Dir,
		Env:     msg.Env,
		Cols:    msg.Cols,
		Rows:    msg.Rows,
	})
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameCreated, session.Info())
}

func (c *serverConn) handleList() {
	infos, err := c.server.supervisor.List(context.Background())
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameListed, infos)
}

func (c *serverConn) handleAttach(payload []byte) bool {
	if c.attachment != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "connection is already attached"})
		return true
	}
	var msg attachMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return true
	}
	session, err := c.server.supervisor.resolveSession(msg.ID)
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return true
	}
	if msg.ExpectIncarnation != "" || msg.ExpectCreatedAt != 0 {
		expected := Identity{CreatedAt: time.Unix(0, msg.ExpectCreatedAt), Incarnation: msg.ExpectIncarnation}
		if !sameIdentity(expected, session.Identity()) {
			c.replyError(frameError, errorMsg{Code: codeIdentity, Message: fmt.Sprintf("%s: %s", ErrIdentity, msg.ID)})
			return true
		}
	}
	attachment, err := session.attach()
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return true
	}
	c.attachment = attachment
	if err := c.reply(frameAttached, attachment.Info()); err != nil {
		_ = attachment.Detach()
		return false
	}
	go c.streamOutput(attachment)
	return true
}

func (c *serverConn) handleInput(payload []byte) {
	if c.attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "input frame without attach"})
		return
	}
	count, err := c.attachment.tryWrite(payload)
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameInputAck, inputAckMsg{Written: count})
}

func (c *serverConn) handleKillSession(payload []byte) {
	if c.attachment != nil {
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
			c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
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
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleResize(payload []byte) {
	if c.attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "resize frame without attach"})
		return
	}
	var msg resizeMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := c.attachment.Resize(context.Background(), msg.Cols, msg.Rows); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleKill() {
	if c.attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "kill frame without attach"})
		return
	}
	if err := c.attachment.Kill(context.Background()); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleGetVersions() {
	if c.attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "versions frame without attach"})
		return
	}
	event, grid, err := c.attachment.Versions()
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameVersions, versionsMsg{Event: event, Grid: grid})
}

func (c *serverConn) handleSetVersions(payload []byte) {
	if c.attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "versions frame without attach"})
		return
	}
	var msg versionsMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := c.attachment.PersistVersions(msg.Event, msg.Grid); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) streamOutput(attachment *Attachment) {
	for {
		buffer := make([]byte, outputChunkSize)
		count, err := attachment.Read(buffer)
		if count > 0 {
			seq := attachment.NextSeq() - uint64(count)
			if writeErr := c.replyRaw(frameOutput, outputPayload(seq, buffer[:count])); writeErr != nil {
				return
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				info := attachment.Info()
				_ = c.reply(frameExit, exitMsg{Code: info.ExitCode, Signal: info.Signal})
			case errors.Is(err, ErrClosed):
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

func (c *serverConn) close() {
	c.closeOnce.Do(func() {
		if c.attachment != nil {
			_ = c.attachment.Detach()
		}
		_ = c.conn.Close()
	})
}
