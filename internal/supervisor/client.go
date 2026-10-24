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

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	maxStreamBuffer                = 64 * 1024 * 1024
	defaultMaxExecOutput           = 4 * 1024 * 1024
	defaultStreamCloseDrainTimeout = 10 * time.Second
)

type DialFunc func(ctx context.Context) (net.Conn, error)

type Client struct {
	socketPath    string
	stateDir      string
	stateDigest   string
	dial          DialFunc
	maxExecOutput int
}

func NewClient(socketPath, stateDir string) *Client {
	return &Client{socketPath: endpointPath(socketPath), stateDir: stateDir, maxExecOutput: defaultMaxExecOutput}
}

// NewRemoteClient returns a Client that speaks the supervisor protocol over
// connections produced by dial, such as an SSH exec channel bridged to the
// remote daemon socket. The state digest of the remote state directory is
// presented at hello; the caller computes it on the remote host.
func NewRemoteClient(dial DialFunc, stateDigest string) *Client {
	return &Client{stateDigest: stateDigest, dial: dial, maxExecOutput: defaultMaxExecOutput}
}

func (c *Client) Create(ctx context.Context, options CreateOptions) (Info, error) {
	if options.Pty != nil && !*options.Pty {
		return Info{}, fmt.Errorf("%w: non-PTY sessions are one-shot; use Client.Exec", ErrInvalidInput)
	}
	if options.Attempt == "" {
		options.Attempt = ids.New()
	}
	conn, err := c.dialConn(ctx)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameCreate, createMsg{
		ID:      options.ID,
		Attempt: options.Attempt,
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

// ExecSpec describes a one-shot non-PTY command: an argv with environment and
// working directory, no input and no resize.
type ExecSpec struct {
	Command []string
	Dir     string
	Env     []string
}

// Exec runs a non-PTY command through the supervisor and collects stdout and
// stderr separately until the process exits. exitCode is the process exit
// code, or -1 when the process was terminated by a signal (signal then carries
// its name); both are meaningful only when err is nil. Output beyond the
// client's limit kills the process and returns the partial buffers with an
// error. Cancelling ctx kills the process (the connection drop makes the
// supervisor terminate the one-shot session).
func (c *Client) Exec(ctx context.Context, spec ExecSpec) (stdout, stderr []byte, exitCode int, signal string, err error) {
	exitCode, signal, err = c.ExecStream(ctx, spec, func(isStderr bool, data []byte) error {
		if isStderr {
			stderr = append(stderr, data...)
		} else {
			stdout = append(stdout, data...)
		}
		if len(stdout)+len(stderr) > c.maxExecOutput {
			return fmt.Errorf("%w: exec output exceeds limit of %d bytes", ErrUnavailable, c.maxExecOutput)
		}
		return nil
	})
	return stdout, stderr, exitCode, signal, err
}

// ExecStream runs a non-PTY command like Exec but hands each output chunk to onOutput as it arrives instead of
// buffering, so long-running commands (tail -F) can be consumed incrementally. onOutput returning an error kills
// the process and ends the call with that error. The data slice is only valid during the callback. Cancelling ctx
// kills the process.
func (c *Client) ExecStream(ctx context.Context, spec ExecSpec, onOutput func(stderr bool, data []byte) error) (exitCode int, signal string, err error) {
	if len(spec.Command) == 0 || spec.Command[0] == "" {
		return -1, "", fmt.Errorf("%w: exec command is required", ErrInvalidInput)
	}
	conn, err := c.dialConn(ctx)
	if err != nil {
		return -1, "", err
	}
	defer func() { _ = conn.Close() }()
	if !slices.Contains(conn.features, featureExec) {
		return -1, "", fmt.Errorf("%w: supervisor does not support non-PTY exec", ErrUnsupported)
	}
	noPTY := false
	kind, _, err := conn.request(ctx, frameCreate, createMsg{
		ID:      ids.New(),
		Command: spec.Command,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Pty:     &noPTY,
	})
	if err != nil {
		return -1, "", err
	}
	if kind != frameCreated {
		return -1, "", protocolMismatch(kind)
	}
	stopWatch := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopWatch()
	for {
		kind, payload, readErr := readFrame(conn.conn)
		if readErr != nil {
			if ctx.Err() != nil {
				return -1, "", context.Cause(ctx)
			}
			return -1, "", fmt.Errorf("%w: read exec stream: %v", base.ErrDisconnected, readErr)
		}
		switch kind {
		case frameExecOutput:
			stream, data, parseErr := parseExecOutput(payload)
			if parseErr != nil {
				return -1, "", parseErr
			}
			if stream != streamStdout && stream != streamStderr {
				return -1, "", fmt.Errorf("%w: exec output stream %d", ErrProtocol, stream)
			}
			if cbErr := onOutput(stream == streamStderr, data); cbErr != nil {
				return -1, "", cbErr
			}
		case frameExit:
			var msg exitMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				return -1, "", err
			}
			code := -1
			if msg.Code != nil {
				code = *msg.Code
			}
			return code, msg.Signal, nil
		case frameError, frameFatal:
			var msg errorMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				return -1, "", err
			}
			return -1, "", decodedError(msg)
		default:
			return -1, "", protocolMismatch(kind)
		}
	}
}

func (c *Client) List(ctx context.Context) ([]Info, error) {
	conn, err := c.dialConn(ctx)
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

func (c *Client) MetadataSupported(ctx context.Context) (bool, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	return slices.Contains(conn.features, featureMetadata), nil
}

func (c *Client) Rename(ctx context.Context, request RenameRequest) (Info, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = conn.Close() }()
	if !slices.Contains(conn.features, featureMetadata) {
		return Info{}, fmt.Errorf("%w: supervisor does not support session metadata", ErrUnsupported)
	}
	kind, payload, err := conn.request(ctx, frameRename, request)
	if err != nil {
		return Info{}, err
	}
	if kind != frameRenamed {
		return Info{}, protocolMismatch(kind)
	}
	var info Info
	if err := unmarshalFrame(payload, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (c *Client) Attach(ctx context.Context, id string, expect *Identity) (*Stream, error) {
	return c.AttachFrom(ctx, id, expect, 0)
}

// AttachFrom asks the daemon to resume the recorded output from the absolute
// byte offset. Daemons that predate the offset field ignore it and replay
// from byte zero; the stream detects either case from the first output frame
// and skips the already-consumed prefix locally, so both honor the offset.
func (c *Client) AttachFrom(ctx context.Context, id string, expect *Identity, offset uint64) (*Stream, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return nil, err
	}
	msg := attachMsg{ID: id, Offset: offset}
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
		conn:              conn,
		info:              info,
		identity:          Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation},
		closeDrainTimeout: defaultStreamCloseDrainTimeout,
		want:              offset,
		reqToken:          make(chan struct{}, 1),
		resp:              make(chan clientFrame, 1),
		newData:           make(chan struct{}, 1),
		exitCh:            make(chan struct{}),
		closed:            make(chan struct{}),
	}
	go stream.readLoop()
	return stream, nil
}

func (c *Client) ReconcileCreate(ctx context.Context, id, attempt string) error {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	kind, _, err := conn.request(ctx, frameKillSession, killSessionMsg{ID: id, Attempt: attempt})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

func (c *Client) Kill(ctx context.Context, id string, expect *Identity) error {
	conn, err := c.dialConn(ctx)
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

func (c *Client) dialConn(ctx context.Context) (*clientConn, error) {
	netConn, err := c.dialNet(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: dial supervisor: %v", ErrUnavailable, err)
	}
	digest := c.stateDigest
	if digest == "" {
		digest, err = stateDigestFor(c.stateDir)
		if err != nil {
			_ = netConn.Close()
			return nil, err
		}
	}
	conn := &clientConn{conn: netConn}
	features := []string{featureExec, featureMetadata, featureRuntimeMetadata, featureActivity}
	if c.dial != nil {
		features = append(features, featureReplayGzip)
	}
	kind, payload, err := conn.request(ctx, frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest, Features: features})
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
	conn.features = slices.Clone(ack.Features)
	if slices.Contains(features, featureRuntimeMetadata) && slices.Contains(conn.features, featureRuntimeMetadata) && ack.Runtime != nil {
		metadata := *ack.Runtime
		if err := validateRuntimeMetadata(metadata, ack.Version); err != nil {
			_ = conn.Close()
			return nil, err
		}
		metadata.Features = slices.Clone(metadata.Features)
		conn.runtimeMetadata = &metadata
	}
	if slices.Contains(features, featureActivity) && slices.Contains(conn.features, featureActivity) && ack.Activity != nil {
		if ack.Activity.PTY < 0 || ack.Activity.Exec < 0 {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: negative daemon activity count", ErrProtocol)
		}
		activity := *ack.Activity
		conn.activity = &activity
	}
	return conn, nil
}

func (c *Client) dialNet(ctx context.Context) (net.Conn, error) {
	if c.dial != nil {
		return c.dial(ctx)
	}
	return dialSocket(ctx, c.socketPath)
}

type clientFrame struct {
	kind    frameType
	payload []byte
}

type clientConn struct {
	conn            net.Conn
	features        []string
	runtimeMetadata *RuntimeMetadata
	activity        *ActivitySnapshot
	writeMu         sync.Mutex
}

func (c *clientConn) request(ctx context.Context, kind frameType, message any) (frameType, []byte, error) {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return 0, nil, err
	}
	return c.requestRaw(ctx, kind, payload)
}

func (c *clientConn) requestRaw(ctx context.Context, kind frameType, payload []byte) (frameType, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, context.Cause(ctx)
	}
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return 0, nil, context.Cause(ctx)
	}
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
			return 0, nil, decodedError(msg)
		}
		return response.kind, response.payload, nil
	case <-ctx.Done():
		// The reply can no longer be matched to this request, so the conn is
		// torn down; this also unblocks the parked reader above.
		_ = c.conn.Close()
		return 0, nil, context.Cause(ctx)
	}
}

func (c *clientConn) Close() error {
	return c.conn.Close()
}

type Stream struct {
	conn              *clientConn
	info              Info
	identity          Identity
	closeDrainTimeout time.Duration

	writeMu sync.Mutex

	reqToken chan struct{}

	mu         sync.Mutex
	readBuf    []byte
	seq        uint64
	want       uint64
	skip       uint64
	started    bool
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
	closeErr  error

	// tokenWaitHook, when non-nil, runs after the initial ctx check and
	// immediately before requestRaw blocks acquiring the request token.
	// Tests use it to observe the token wait deterministically.
	tokenWaitHook func()
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
	return s.WriteContext(context.Background(), p)
}

// WriteContext writes p to the session's input pipeline, with ctx governing
// each chunk's acknowledgement. Cancelling ctx fails the stream: the
// cancelled chunk may or may not have reached the supervisor, and the late
// acknowledgement can no longer be told apart from the next response, so
// the input pipeline is withdrawn rather than restored: the stream never
// replays the unconfirmed chunk, and the caller must re-attach to resume.
// A request cancelled before its frame reaches the wire leaves the stream
// healthy. The returned count covers only acknowledged chunks.
func (s *Stream) WriteContext(ctx context.Context, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxFramePayload {
			chunk = chunk[:maxFramePayload]
		}
		kind, payload, err := s.requestRaw(ctx, frameInput, chunk)
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
		select {
		case s.reqToken <- struct{}{}:
		case <-time.After(s.closeDrainTimeout):
			_ = s.conn.Close()
			s.reqToken <- struct{}{}
		}
		defer func() { <-s.reqToken }()
		s.mu.Lock()
		readErr := s.readErr
		s.mu.Unlock()
		s.writeMu.Lock()
		detachErr := writeFrame(s.conn.conn, frameDetach, []byte("{}"))
		s.writeMu.Unlock()
		closeErr := s.conn.Close()
		switch {
		case readErr != nil:
			// The stream already failed: the close inherits that evidence
			// rather than reporting success over a dead supervisor.
			s.closeErr = readErr
		case detachErr != nil && !errors.Is(detachErr, net.ErrClosed):
			s.closeErr = fmt.Errorf("%w: detach supervisor stream: %v", base.ErrDisconnected, detachErr)
		case closeErr != nil && !errors.Is(closeErr, net.ErrClosed):
			s.closeErr = closeErr
		}
	})
	return s.closeErr
}

func (s *Stream) request(ctx context.Context, kind frameType, message any) (frameType, []byte, error) {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return 0, nil, err
	}
	return s.requestRaw(ctx, kind, payload)
}

func (s *Stream) requestRaw(ctx context.Context, kind frameType, payload []byte) (frameType, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, context.Cause(ctx)
	}
	if s.tokenWaitHook != nil {
		s.tokenWaitHook()
	}
	select {
	case s.reqToken <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, context.Cause(ctx)
	}
	defer func() { <-s.reqToken }()
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
	// fail records readErr before the conn dies and before readLoop exits,
	// and Close records userClosed before draining; consulting the state
	// closes the gap where the conn is already gone but the closed channel
	// has not fired yet.
	s.mu.Lock()
	userClosed := s.userClosed
	readErr := s.readErr
	s.mu.Unlock()
	if userClosed {
		return 0, nil, ErrClosed
	}
	if readErr != nil {
		return 0, nil, readErr
	}
	// The token wait can outlive the ctx; re-check before the frame reaches
	// the wire so a withdrawn request never touches the input pipeline.
	if err := ctx.Err(); err != nil {
		return 0, nil, context.Cause(ctx)
	}
	s.mu.Lock()
	s.pending = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pending = false
		s.mu.Unlock()
	}()
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		s.fail(context.Cause(ctx))
		close(cancelDone)
	})
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()
	s.writeMu.Lock()
	err := writeFrame(s.conn.conn, kind, payload)
	s.writeMu.Unlock()
	if err != nil {
		s.mu.Lock()
		readErr := s.readErr
		s.mu.Unlock()
		if readErr != nil {
			return 0, nil, readErr
		}
		return 0, nil, fmt.Errorf("%w: send frame: %v", base.ErrDisconnected, err)
	}
	select {
	case response := <-s.resp:
		if response.kind == frameError {
			var msg errorMsg
			if err := unmarshalFrame(response.payload, &msg); err != nil {
				return 0, nil, err
			}
			return 0, nil, decodedError(msg)
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
		case frameOutput, frameReplayGzip:
			var seq uint64
			var data []byte
			if kind == frameReplayGzip {
				if !slices.Contains(s.conn.features, featureReplayGzip) {
					s.fail(fmt.Errorf("%w: unnegotiated compressed replay", ErrProtocol))
					return
				}
				seq, data, err = parseReplayGzip(payload)
			} else {
				seq, data, err = parseOutput(payload)
			}
			if err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			if !s.started {
				s.started = true
				if seq > s.want {
					s.mu.Unlock()
					s.fail(fmt.Errorf("%w: output stream starts at %d, beyond resume offset %d", ErrProtocol, seq, s.want))
					return
				}
				s.seq = seq
				s.skip = s.want - seq
			}
			if seq != s.seq {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: output sequence %d does not match expected %d", ErrProtocol, seq, s.seq))
				return
			}
			s.seq += uint64(len(data))
			if s.skip > 0 {
				drop := uint64(len(data))
				if drop > s.skip {
					drop = s.skip
				}
				data = data[drop:]
				s.skip -= drop
			}
			if len(s.readBuf)+len(data) > maxStreamBuffer {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: stream buffer overflow", ErrProtocol))
				return
			}
			s.readBuf = append(s.readBuf, data...)
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
			s.fail(decodedError(msg))
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
				s.fail(decodedError(msg))
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
