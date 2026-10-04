package supervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const maxStreamBuffer = 64 * 1024 * 1024

type Client struct {
	socketPath string
}

func NewClient(socketPath string) *Client {
	if absolute, err := filepath.Abs(socketPath); err == nil {
		socketPath = absolute
	}
	return &Client{socketPath: socketPath}
}

func (c *Client) Create(ctx context.Context, options CreateOptions) (Info, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameCreate, createMsg{
		ID:      options.ID,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,
	})
	if err != nil {
		return Info{}, err
	}
	if kind != frameCreated {
		return Info{}, protocolMismatch(kind)
	}
	var info Info
	if err := unmarshalFrame(payload, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (c *Client) List(ctx context.Context) ([]Info, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameList, struct{}{})
	if err != nil {
		return nil, err
	}
	if kind != frameListed {
		return nil, protocolMismatch(kind)
	}
	var infos []Info
	if err := unmarshalFrame(payload, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

func (c *Client) Attach(ctx context.Context, id string, expect *Identity) (*Stream, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	msg := attachMsg{ID: id}
	if expect != nil {
		msg.ExpectCreatedAt = expect.CreatedAt.UnixNano()
		msg.ExpectIncarnation = expect.Incarnation
	}
	kind, payload, err := conn.request(ctx, frameAttach, msg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if kind != frameAttached {
		_ = conn.Close()
		return nil, protocolMismatch(kind)
	}
	var info Info
	if err := unmarshalFrame(payload, &info); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stream := &Stream{
		conn:     conn,
		info:     info,
		identity: Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation},
		resp:     make(chan clientFrame, 1),
		newData:  make(chan struct{}, 1),
		exitCh:   make(chan struct{}),
		closed:   make(chan struct{}),
	}
	go stream.readLoop()
	return stream, nil
}

func (c *Client) Kill(ctx context.Context, id string, expect *Identity) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	msg := killSessionMsg{ID: id}
	if expect != nil {
		msg.ExpectCreatedAt = expect.CreatedAt.UnixNano()
		msg.ExpectIncarnation = expect.Incarnation
	}
	kind, _, err := conn.request(ctx, frameKillSession, msg)
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

func (c *Client) dial(ctx context.Context) (*clientConn, error) {
	netConn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: dial supervisor: %v", ErrUnavailable, err)
	}
	conn := &clientConn{conn: netConn}
	kind, payload, err := conn.request(ctx, frameHello, helloMsg{Version: ProtocolVersion})
	if err != nil {
		_ = netConn.Close()
		return nil, err
	}
	if kind != frameHelloAck {
		_ = netConn.Close()
		return nil, protocolMismatch(kind)
	}
	var ack helloMsg
	if err := unmarshalFrame(payload, &ack); err != nil {
		_ = netConn.Close()
		return nil, err
	}
	if ack.Version != ProtocolVersion {
		_ = netConn.Close()
		return nil, fmt.Errorf("%w: server speaks protocol %d", ErrProtocol, ack.Version)
	}
	return conn, nil
}

type clientFrame struct {
	kind    frameType
	payload []byte
}

type clientConn struct {
	conn    net.Conn
	writeMu sync.Mutex
}

func (c *clientConn) request(ctx context.Context, kind frameType, message any) (frameType, []byte, error) {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return 0, nil, err
	}
	return c.requestRaw(ctx, kind, payload)
}

func (c *clientConn) requestRaw(ctx context.Context, kind frameType, payload []byte) (frameType, []byte, error) {
	c.writeMu.Lock()
	err := writeFrame(c.conn, kind, payload)
	c.writeMu.Unlock()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: send frame: %v", base.ErrDisconnected, err)
	}
	type result struct {
		kind    frameType
		payload []byte
		err     error
	}
	results := make(chan result, 1)
	go func() {
		responseKind, responsePayload, err := readFrame(c.conn)
		results <- result{responseKind, responsePayload, err}
	}()
	select {
	case response := <-results:
		if response.err != nil {
			return 0, nil, fmt.Errorf("%w: read frame: %v", base.ErrDisconnected, response.err)
		}
		if response.kind == frameError {
			var msg errorMsg
			if err := unmarshalFrame(response.payload, &msg); err != nil {
				return 0, nil, err
			}
			return 0, nil, codedError(msg.Code, msg.Message)
		}
		return response.kind, response.payload, nil
	case <-ctx.Done():
		return 0, nil, context.Cause(ctx)
	}
}

func (c *clientConn) Close() error {
	return c.conn.Close()
}

type Stream struct {
	conn     *clientConn
	info     Info
	identity Identity

	writeMu sync.Mutex
	reqMu   sync.Mutex

	mu         sync.Mutex
	readBuf    []byte
	seq        uint64
	exitErr    error
	dead       bool
	userClosed bool
	readErr    error
	pending    bool

	resp      chan clientFrame
	newData   chan struct{}
	exitCh    chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func (s *Stream) Info() Info {
	return s.info
}

func (s *Stream) Identity() Identity {
	return s.identity
}

func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if s.userClosed {
			s.mu.Unlock()
			return 0, ErrClosed
		}
		if len(s.readBuf) > 0 {
			count := copy(p, s.readBuf)
			s.readBuf = s.readBuf[count:]
			s.mu.Unlock()
			return count, nil
		}
		dead := s.dead
		readErr := s.readErr
		s.mu.Unlock()
		if readErr != nil {
			return 0, readErr
		}
		if dead {
			return 0, io.EOF
		}
		select {
		case <-s.newData:
		case <-s.exitCh:
		case <-s.closed:
		}
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxFramePayload {
			chunk = chunk[:maxFramePayload]
		}
		kind, payload, err := s.requestRaw(context.Background(), frameInput, chunk)
		if err != nil {
			return written, err
		}
		if kind != frameInputAck {
			return written, protocolMismatch(kind)
		}
		var ack inputAckMsg
		if err := unmarshalFrame(payload, &ack); err != nil {
			return written, err
		}
		written += ack.Written
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *Stream) Resize(ctx context.Context, cols, rows uint32) error {
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	kind, _, err := s.request(ctx, frameResize, resizeMsg{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

func (s *Stream) Kill(ctx context.Context) error {
	kind, _, err := s.request(ctx, frameKill, struct{}{})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return s.Close()
}

func (s *Stream) Wait(ctx context.Context) error {
	s.mu.Lock()
	userClosed := s.userClosed
	s.mu.Unlock()
	if userClosed {
		return ErrClosed
	}
	select {
	case <-s.exitCh:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.exitErr
	case <-s.closed:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.dead {
			return s.exitErr
		}
		if s.readErr != nil {
			return s.readErr
		}
		return base.ErrDisconnected
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Stream) Versions() (eventVersion, gridRevision uint64, err error) {
	kind, payload, err := s.request(context.Background(), frameGetVersions, struct{}{})
	if err != nil {
		return 0, 0, err
	}
	if kind != frameVersions {
		return 0, 0, protocolMismatch(kind)
	}
	var msg versionsMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		return 0, 0, err
	}
	return msg.Event, msg.Grid, nil
}

func (s *Stream) PersistVersions(eventVersion, gridRevision uint64) error {
	kind, _, err := s.request(context.Background(), frameSetVersions, versionsMsg{Event: eventVersion, Grid: gridRevision})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.userClosed = true
		s.mu.Unlock()
		s.writeMu.Lock()
		_ = writeFrame(s.conn.conn, frameDetach, []byte("{}"))
		s.writeMu.Unlock()
		_ = s.conn.Close()
	})
	return nil
}

func (s *Stream) request(ctx context.Context, kind frameType, message any) (frameType, []byte, error) {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return 0, nil, err
	}
	return s.requestRaw(ctx, kind, payload)
}

func (s *Stream) requestRaw(ctx context.Context, kind frameType, payload []byte) (frameType, []byte, error) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	select {
	case <-s.closed:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.userClosed {
			return 0, nil, ErrClosed
		}
		if s.readErr != nil {
			return 0, nil, s.readErr
		}
		return 0, nil, base.ErrDisconnected
	default:
	}
	s.mu.Lock()
	s.pending = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pending = false
		s.mu.Unlock()
	}()
	s.writeMu.Lock()
	err := writeFrame(s.conn.conn, kind, payload)
	s.writeMu.Unlock()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: send frame: %v", base.ErrDisconnected, err)
	}
	select {
	case response := <-s.resp:
		if response.kind == frameError {
			var msg errorMsg
			if err := unmarshalFrame(response.payload, &msg); err != nil {
				return 0, nil, err
			}
			return 0, nil, codedError(msg.Code, msg.Message)
		}
		return response.kind, response.payload, nil
	case <-ctx.Done():
		s.fail(context.Cause(ctx))
		return 0, nil, context.Cause(ctx)
	case <-s.closed:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.readErr != nil {
			return 0, nil, s.readErr
		}
		return 0, nil, base.ErrDisconnected
	}
}

func (s *Stream) readLoop() {
	defer close(s.closed)
	for {
		kind, payload, err := readFrame(s.conn.conn)
		if err != nil {
			s.mu.Lock()
			dead := s.dead
			s.mu.Unlock()
			if dead {
				return
			}
			s.fail(fmt.Errorf("%w: supervisor stream ended: %v", base.ErrDisconnected, err))
			return
		}
		switch kind {
		case frameOutput:
			seq, data, err := parseOutput(payload)
			if err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			if seq != s.seq {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: output sequence %d does not match expected %d", ErrProtocol, seq, s.seq))
				return
			}
			if len(s.readBuf)+len(data) > maxStreamBuffer {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: stream buffer overflow", ErrProtocol))
				return
			}
			s.readBuf = append(s.readBuf, data...)
			s.seq += uint64(len(data))
			s.mu.Unlock()
			select {
			case s.newData <- struct{}{}:
			default:
			}
		case frameExit:
			var msg exitMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			s.exitErr = streamExitError(msg)
			s.dead = true
			s.mu.Unlock()
			select {
			case <-s.exitCh:
			default:
				close(s.exitCh)
			}
		case frameFatal:
			var msg errorMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				s.fail(err)
				return
			}
			s.fail(codedError(msg.Code, msg.Message))
			return
		case frameError:
			var msg errorMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			pending := s.pending
			s.mu.Unlock()
			if !pending {
				s.fail(codedError(msg.Code, msg.Message))
				return
			}
			select {
			case s.resp <- clientFrame{kind: kind, payload: payload}:
			default:
				s.fail(fmt.Errorf("%w: unsolicited error frame", ErrProtocol))
				return
			}
		default:
			select {
			case s.resp <- clientFrame{kind: kind, payload: payload}:
			default:
				s.fail(fmt.Errorf("%w: unsolicited response frame %d", ErrProtocol, kind))
				return
			}
		}
	}
}

func (s *Stream) fail(err error) {
	s.mu.Lock()
	if s.readErr == nil {
		s.readErr = err
	}
	s.mu.Unlock()
	_ = s.conn.Close()
}

func streamExitError(msg exitMsg) error {
	if msg.Signal == "" && (msg.Code == nil || *msg.Code == 0) {
		return nil
	}
	code := 0
	if msg.Code != nil {
		code = *msg.Code
	} else if msg.Signal != "" {
		code = -1
	}
	return &base.ExitError{Code: code, Signal: msg.Signal}
}

func protocolMismatch(kind frameType) error {
	return fmt.Errorf("%w: unexpected response frame %d", ErrProtocol, kind)
}
