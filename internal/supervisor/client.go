package supervisor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
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
	// compressOutput asks the helper for compressed replay and live output;
	// remote clients set it because their bytes cross a network.
	compressOutput bool
	// rules, when set, names the rule owner in every hello and is pushed to
	// the helper when the acknowledgement reports another revision.
	rules StateRuleSource
}

// WithStateRules returns a copy of the client that acts for the rule owner
// of source: every connection it opens names the owner in its hello, so the
// sessions it creates are evaluated with that owner's rules, and pushes the
// owner's rules when the helper reports another revision. A push the helper
// rejects is reported to source and does not fail the connection. A source
// with a malformed owner key is ignored.
func (c *Client) WithStateRules(source StateRuleSource) *Client {
	copied := *c
	copied.rules = source
	return &copied
}

// SyncStateRules connects to the helper once so it takes the current state
// rules, and returns the push error, including a rejection. It does nothing
// without a state rule source.
func (c *Client) SyncStateRules(ctx context.Context) error {
	if c.rulesOwner() == "" {
		return nil
	}
	conn, err := c.dialHello(ctx, false)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return c.pushStateRules(ctx, conn)
}

// rulesOwner is the owner key the client sends in its hello; empty without a
// source or for a malformed key.
func (c *Client) rulesOwner() string {
	if c.rules == nil {
		return ""
	}
	if key := c.rules.StateRulesOwner(); validStateRulesOwner(key) {
		return key
	}
	return ""
}

// pushStateRules sends the source's rules unless the helper already has
// their revision, and reports the outcome to the source.
func (c *Client) pushStateRules(ctx context.Context, conn *clientConn) error {
	set, ok := c.rules.StateRules()
	if !ok {
		return nil
	}
	if set.Revision == conn.stateRules {
		c.rules.StateRulesPushed(set.Revision, nil)
		return nil
	}
	kind, _, err := conn.request(ctx, frameSetStateRules, set)
	if err == nil && kind != frameOK {
		err = protocolMismatch(kind)
	}
	if err == nil {
		conn.stateRules = set.Revision
	}
	c.rules.StateRulesPushed(set.Revision, err)
	return err
}

func NewClient(socketPath, stateDir string) *Client {
	return &Client{socketPath: endpointPath(socketPath), stateDir: stateDir, maxExecOutput: defaultMaxExecOutput}
}

// NewRemoteClient returns a Client that speaks the supervisor protocol over
// connections produced by dial, such as an SSH exec channel bridged to the
// remote daemon socket. The state digest of the remote state directory is
// presented at hello; the caller computes it on the remote host.
func NewRemoteClient(dial DialFunc, stateDigest string) *Client {
	return &Client{stateDigest: stateDigest, dial: dial, maxExecOutput: defaultMaxExecOutput, compressOutput: true}
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

		HistoryLimit: options.HistoryLimit,
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

// CreateAttach creates a PTY session and attaches to it on one connection,
// saving the second connection (a full exec channel and hello for remote
// helpers).
//
// On error, a non-empty Info.ID means the session was created and the caller
// must remove it; an empty one means the outcome of the create is unknown (or
// definitely rejected, see definiteCreateRejection).
func (c *Client) CreateAttach(ctx context.Context, options CreateOptions) (Info, *Stream, error) {
	if options.Pty != nil && !*options.Pty {
		return Info{}, nil, fmt.Errorf("%w: non-PTY sessions are one-shot; use Client.Exec", ErrInvalidInput)
	}
	if options.Attempt == "" {
		options.Attempt = ids.New()
	}
	conn, err := c.dialConn(ctx)
	if err != nil {
		return Info{}, nil, err
	}
	payload, err := marshalFrame(frameCreate, createMsg{
		ID:      options.ID,
		Attempt: options.Attempt,
		Command: options.Command,
		Dir:     options.Dir,
		Env:     options.Env,
		Cols:    options.Cols,
		Rows:    options.Rows,

		HistoryLimit: options.HistoryLimit,
	})
	if err != nil {
		_ = conn.Close()
		return Info{}, nil, err
	}
	if err := conn.send(ctx, frameCreate, payload); err != nil {
		_ = conn.Close()
		return Info{}, nil, err
	}
	// Note: if the create fails (for example the ID already exists), the
	// pipelined attach may briefly attach to that existing session and start
	// replaying it (up to 4 MiB with output_credit) until the client closes the
	// connection after reading the create error; nothing is consumed or retired.
	// With a caller-chosen ID the attach can follow the create without
	// waiting for its reply; the identity is verified once both arrive.
	pipelined := options.ID != ""
	if pipelined {
		if err := conn.sendJSON(ctx, frameAttach, attachMsg{ID: options.ID, ExpectGeneration: conn.generation}); err != nil {
			_ = conn.Close()
			return Info{}, nil, err
		}
	}
	info, err := conn.awaitInfo(ctx, frameCreated)
	if err != nil {
		_ = conn.Close()
		return Info{}, nil, err
	}
	if !pipelined {
		msg := attachMsg{ID: info.ID, ExpectCreatedAt: info.CreatedAt.UnixNano(), ExpectIncarnation: info.Incarnation, ExpectGeneration: info.Generation}
		if err := conn.sendJSON(ctx, frameAttach, msg); err != nil {
			_ = conn.Close()
			return info, nil, err
		}
	}
	reply, err := conn.awaitAttached(ctx)
	if err != nil {
		_ = conn.Close()
		return info, nil, err
	}
	attached := reply.Info
	if attached.ID != info.ID || attached.Incarnation != info.Incarnation || attached.Generation != info.Generation || !attached.CreatedAt.Equal(info.CreatedAt) {
		_ = conn.Close()
		return info, nil, fmt.Errorf("%w: attached session is not the one just created", ErrIdentity)
	}
	return info, c.newStream(conn, reply), nil
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
	return c.list(ctx, listOptions{})
}

// ListWithEvents is List with each session's retained state events after
// the given sequence. Sequences are shared by all sessions of one helper
// generation; pass zero after a generation change.
func (c *Client) ListWithEvents(ctx context.Context, after uint64) ([]Info, error) {
	return c.list(ctx, listOptions{Events: true, EventsAfter: after})
}

func (c *Client) list(ctx context.Context, options listOptions) ([]Info, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameList, options)
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

func (c *Client) Rename(ctx context.Context, request RenameRequest) (Info, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = conn.Close() }()
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
// byte offset. The stream checks the first output frame against the offset
// and skips any already-consumed prefix locally. An offset the helper no
// longer retains resumes after the history base; Stream.Replay reports it.
func (c *Client) AttachFrom(ctx context.Context, id string, expect *Identity, offset uint64) (*Stream, error) {
	return c.AttachWithOptions(ctx, id, expect, AttachOptions{Offset: offset})
}

// AttachWithOptions attaches with an explicit replay plan; see AttachOptions.
func (c *Client) AttachWithOptions(ctx context.Context, id string, expect *Identity, options AttachOptions) (*Stream, error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return nil, err
	}
	msg := attachMsg{ID: id, Offset: options.Offset, Tail: options.Tail, Retain: options.Retain}
	if expect != nil {
		msg.ExpectCreatedAt = expect.CreatedAt.UnixNano()
		msg.ExpectIncarnation = expect.Incarnation
		msg.ExpectGeneration = expect.Generation
	}
	if err := conn.sendJSON(ctx, frameAttach, msg); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reply, err := conn.awaitAttached(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if options.Tail == 0 && !reply.Replay.Truncated && reply.Replay.ReplayFrom != options.Offset {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: helper resumed at %d instead of %d", ErrProtocol, reply.Replay.ReplayFrom, options.Offset)
	}
	return c.newStream(conn, reply), nil
}

// ReadHistory returns recorded output of the session ending at before, at
// most maxBytes (bounded by MaxHistoryRead), without attaching. expect is
// required.
func (c *Client) ReadHistory(ctx context.Context, id string, expect Identity, before uint64, maxBytes int) (HistoryChunk, error) {
	if expect.Incarnation == "" {
		return HistoryChunk{}, fmt.Errorf("%w: history read requires the session identity", ErrInvalidInput)
	}
	conn, err := c.dialConn(ctx)
	if err != nil {
		return HistoryChunk{}, err
	}
	defer func() { _ = conn.Close() }()
	kind, payload, err := conn.request(ctx, frameReadHistory, readHistoryMsg{
		ID: id, ExpectCreatedAt: expect.CreatedAt.UnixNano(), ExpectIncarnation: expect.Incarnation,
		ExpectGeneration: expect.Generation, Before: before, MaxBytes: maxBytes,
	})
	if err != nil {
		return HistoryChunk{}, err
	}
	if kind != frameHistory {
		return HistoryChunk{}, protocolMismatch(kind)
	}
	chunk, err := parseHistory(payload)
	if err != nil {
		return HistoryChunk{}, err
	}
	if chunk.End > before || len(chunk.Data) > MaxHistoryRead {
		return HistoryChunk{}, fmt.Errorf("%w: history reply exceeds the request", ErrProtocol)
	}
	return chunk, nil
}

func (c *Client) newStream(conn *clientConn, reply attachedMsg) *Stream {
	info := reply.Info
	stream := &Stream{
		conn:              conn,
		info:              info,
		replay:            reply.Replay,
		identity:          Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation, Generation: info.Generation},
		closeDrainTimeout: defaultStreamCloseDrainTimeout,
		want:              reply.Replay.ReplayFrom,
		consumed:          reply.Replay.ReplayFrom,
		reqToken:          make(chan struct{}, 1),
		resp:              make(chan clientFrame, 1),
		newData:           make(chan struct{}, 1),
		exitCh:            make(chan struct{}),
		closed:            make(chan struct{}),
		inputToken:        make(chan struct{}, 1),
		inputWake:         make(chan struct{}),
		creditWake:        make(chan struct{}, 1),
		stateWake:         make(chan struct{}, 1),
	}
	if info.State != nil {
		state := *info.State
		state.Events = nil
		stream.state = &state
	}
	go stream.readLoop()
	go stream.creditLoop()
	return stream
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
	return c.dialHello(ctx, true)
}

// dialHello connects and says hello; with push it also brings the helper
// to the rule owner's current rules.
func (c *Client) dialHello(ctx context.Context, push bool) (*clientConn, error) {
	owner := c.rulesOwner()
	netConn, err := c.dialNet(ctx)
	if err != nil {
		if legacyErr := c.legacyHelperError(ctx); legacyErr != nil {
			return nil, legacyErr
		}
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
	conn := &clientConn{conn: netConn, compress: c.compressOutput}
	kind, payload, err := conn.request(ctx, frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest, CompressOutput: conn.compress, StateRulesOwner: owner})
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
		return nil, versionMismatchError(ack.Version, "hello acknowledged with another protocol version")
	}
	if ack.Runtime != nil {
		metadata := *ack.Runtime
		if err := validateRuntimeMetadata(metadata, ack.Version); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn.runtimeMetadata = &metadata
	}
	if ack.Generation == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: hello acknowledgement lacks the helper generation", ErrProtocol)
	}
	conn.generation = ack.Generation
	if ack.Activity == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: hello acknowledgement lacks daemon activity", ErrProtocol)
	}
	if ack.Activity.PTY < 0 || ack.Activity.Exec < 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: negative daemon activity count", ErrProtocol)
	}
	conn.activity = *ack.Activity
	if ack.StateRules == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: hello acknowledgement lacks the state rule revision", ErrProtocol)
	}
	conn.stateRules = ack.StateRules.Revision
	if push && owner != "" {
		// A rejected push keeps the connection usable; a failed exchange
		// leaves it broken.
		var remote *wireError
		if err := c.pushStateRules(ctx, conn); err != nil && !errors.As(err, &remote) {
			_ = conn.Close()
			return nil, err
		}
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
	conn net.Conn
	// compress is the hello preference: only then may the server send
	// frameReplayGzip and frameOutputZstd.
	compress bool
	// generation is the helper process this connection reached.
	generation      string
	runtimeMetadata *RuntimeMetadata
	activity        ActivitySnapshot
	// stateRules is the helper's state rule revision for the hello's owner,
	// empty for none.
	stateRules string
	writeMu    sync.Mutex
}

func (c *clientConn) request(ctx context.Context, kind frameType, message any) (frameType, []byte, error) {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return 0, nil, err
	}
	return c.requestRaw(ctx, kind, payload)
}

func (c *clientConn) requestRaw(ctx context.Context, kind frameType, payload []byte) (frameType, []byte, error) {
	if err := c.send(ctx, kind, payload); err != nil {
		return 0, nil, err
	}
	return c.await(ctx)
}

func (c *clientConn) sendJSON(ctx context.Context, kind frameType, message any) error {
	payload, err := marshalFrame(kind, message)
	if err != nil {
		return err
	}
	return c.send(ctx, kind, payload)
}

// awaitAttached waits for the attach reply.
func (c *clientConn) awaitAttached(ctx context.Context) (attachedMsg, error) {
	kind, payload, err := c.await(ctx)
	if err != nil {
		return attachedMsg{}, err
	}
	if kind != frameAttached {
		return attachedMsg{}, protocolMismatch(kind)
	}
	var reply attachedMsg
	if err := unmarshalFrame(payload, &reply); err != nil {
		return attachedMsg{}, err
	}
	replay := reply.Replay
	if replay.HistoryBase > replay.ReplayFrom || replay.ReplayFrom > replay.DisplayFrom || replay.DisplayFrom > replay.HistoryNext {
		return attachedMsg{}, fmt.Errorf("%w: inconsistent attach replay offsets", ErrProtocol)
	}
	return reply, nil
}

// awaitInfo waits for the next response, which must be a want frame carrying Info.
func (c *clientConn) awaitInfo(ctx context.Context, want frameType) (Info, error) {
	kind, payload, err := c.await(ctx)
	if err != nil {
		return Info{}, err
	}
	if kind != want {
		return Info{}, protocolMismatch(kind)
	}
	var info Info
	if err := unmarshalFrame(payload, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (c *clientConn) send(ctx context.Context, kind frameType, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return context.Cause(ctx)
	}
	err := writeFrame(c.conn, kind, payload)
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: send frame: %v", base.ErrDisconnected, err)
	}
	return nil
}

// await reads the next response frame; responses arrive in request order.
func (c *clientConn) await(ctx context.Context) (frameType, []byte, error) {
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
	replay            ReplayInfo
	identity          Identity
	closeDrainTimeout time.Duration

	writeMu sync.Mutex

	reqToken chan struct{}

	mu      sync.Mutex
	readBuf []byte
	seq     uint64
	want    uint64
	skip    uint64
	// consumed is the absolute offset of the next byte Read returns.
	// buffered counts every byte appended to readBuf and taken the bytes
	// Read returned; gapMarks are history gaps positioned in that count.
	consumed   uint64
	buffered   uint64
	taken      uint64
	gapMarks   []streamGap
	gaps       uint64
	started    bool
	exitErr    error
	dead       bool
	userClosed bool
	// killed is set once the supervisor confirmed a Kill: input it rejects
	// afterwards is a consequence of that kill, not a close failure.
	killed  bool
	readErr error
	pending bool

	resp      chan clientFrame
	newData   chan struct{}
	exitCh    chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error

	// Windowed input. inflight holds the sizes of frames on the
	// wire that have no acknowledgement yet, oldest first; inputErr is the
	// first rejection reported by the server. All three are guarded by mu.
	// inputToken serializes writers so reservation order equals wire order.
	inputToken    chan struct{}
	inflight      []int
	inflightBytes int
	inputErr      error
	// inputErrUnacked is inflightBytes when inputErr was recorded: input sent
	// before the rejection was answered is not counted as written, however
	// late the writer observes it.
	inputErrUnacked int
	inputWake       chan struct{}

	// Output credit. retired counts stream bytes the consumer has read or
	// skipped; creditSent is what the server has been told.
	retired    uint64
	creditSent uint64
	creditWake chan struct{}

	// peakBuffered is the largest unread backlog seen (guarded by mu); tests
	// assert it stays within the credit window.
	peakBuffered int
	// windowWaitHook, when non-nil, runs just before reserveInput parks for
	// window space. Tests use it to observe the wait deterministically.
	windowWaitHook func()

	// state is the latest attention state pushed by the helper, nil when
	// it reports none; stateEvents are the events pushed since attach,
	// at most maxStateEvents. Both are guarded by mu.
	state       *SessionState
	stateEvents []SessionEvent
	stateWake   chan struct{}

	// tokenWaitHook, when non-nil, runs immediately before requestRaw blocks
	// acquiring the request token (after its initial ctx check) and before a
	// write blocks acquiring the input token. Tests use it to observe the
	// token wait deterministically.
	tokenWaitHook func()
}

func (s *Stream) Info() Info {
	return s.info
}

// Replay reports where the stream started.
func (s *Stream) Replay() ReplayInfo {
	return s.replay
}

// ConsumedOffset returns the absolute offset just past the bytes Read has
// returned. A Read never spans a history gap, so after a Read the offset is
// the end of the returned bytes; it jumps past dropped output on the Read
// that crosses the gap.
func (s *Stream) ConsumedOffset() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumed
}

// Gaps returns how many history gaps the stream crossed.
func (s *Stream) Gaps() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gaps
}

// streamGap is a history gap at a position of the buffered byte count.
type streamGap struct {
	at uint64
	to uint64
}

// applyGapsLocked crosses every gap at the current read position.
func (s *Stream) applyGapsLocked() {
	for len(s.gapMarks) > 0 && s.gapMarks[0].at == s.taken {
		s.consumed = s.gapMarks[0].to
		s.gapMarks = s.gapMarks[1:]
	}
}

// SessionState returns the latest attention state the helper pushed, with
// the events received since attach (at most the newest maxStateEvents), or
// nil when the helper reports no state.
func (s *Stream) SessionState() *SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil
	}
	state := *s.state
	state.Events = append([]SessionEvent(nil), s.stateEvents...)
	return &state
}

// applyStateLocked records a pushed state. A drop stays reported: the
// events held since attach are incomplete once any were lost.
func (s *Stream) applyStateLocked(state SessionState) {
	s.stateEvents = append(s.stateEvents, state.Events...)
	dropped := state.EventsDropped || s.state != nil && s.state.EventsDropped
	if extra := len(s.stateEvents) - maxStateEvents; extra > 0 {
		s.stateEvents = append([]SessionEvent(nil), s.stateEvents[extra:]...)
		dropped = true
	}
	state.Events = nil
	state.EventsDropped = dropped
	s.state = &state
}

// StateUpdates is signalled after each pushed state. Signals coalesce.
func (s *Stream) StateUpdates() <-chan struct{} {
	return s.stateWake
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
		s.applyGapsLocked()
		if len(s.readBuf) > 0 {
			limit := p
			if len(s.gapMarks) > 0 && s.gapMarks[0].at-s.taken < uint64(len(limit)) {
				// Read never spans a gap, so ConsumedOffset stays exact.
				limit = limit[:s.gapMarks[0].at-s.taken]
			}
			count := copy(limit, s.readBuf)
			s.taken += uint64(count)
			s.consumed += uint64(count)
			s.readBuf = s.readBuf[count:]
			if len(s.readBuf) == 0 && cap(s.readBuf) > 1<<20 {
				// Release the backing array so a drained burst does not stay resident.
				s.readBuf = nil
			}
			s.retireLocked(uint64(count))
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

// WriteContext writes p to the session's input pipeline and returns once the
// supervisor has acknowledged all of it.
//
// The chunks of one call are pipelined inside a bounded window
// (inputWindowBytes), so a large write costs about one round trip per window
// instead of per chunk; the call still returns only after every
// acknowledgement:
//   - ctx governs the acknowledgement wait. Cancelling it fails the stream: the
//     unconfirmed chunks may or may not have reached the supervisor and their
//     late acknowledgements can no longer be told apart from later responses,
//     so the stream never replays them and the caller must re-attach.
//   - A request cancelled before its frame reaches the wire (including while
//     waiting for window space) leaves the stream healthy.
//   - The returned count covers only acknowledged bytes; a rejection reported
//     by the supervisor is returned as the error.
func (s *Stream) WriteContext(ctx context.Context, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return s.writeWindowed(ctx, p, true)
}

// WriteWindowed is the non-waiting form of WriteContext: it returns once the
// data is on the wire inside the window, without waiting for
// acknowledgements, so a caller can keep many small writes in flight. n counts
// bytes fully written to the connection. A rejection of earlier data is
// returned by the next call (n == 0), FlushInput and Close; it does not fail
// the stream. Cancelling ctx while waiting for window space sends nothing for
// the unsent part and leaves the stream healthy; cancelling during a frame
// write fails the stream as in WriteContext.
func (s *Stream) WriteWindowed(ctx context.Context, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return s.writeWindowed(ctx, p, false)
}

func (s *Stream) writeWindowed(ctx context.Context, p []byte, wait bool) (int, error) {
	if s.tokenWaitHook != nil {
		s.tokenWaitHook()
	}
	select {
	case s.inputToken <- struct{}{}:
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	case <-s.closed:
		return 0, s.inputStateError()
	}
	defer func() { <-s.inputToken }()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxInputData {
			chunk = chunk[:maxInputData]
		}
		if err := s.reserveInput(ctx, len(chunk)); err != nil {
			return written, err
		}
		if err := s.sendInput(ctx, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	if wait {
		return s.awaitInputAcks(ctx, written)
	}
	return written, nil
}

// awaitInputAcks blocks until every in-flight frame is acknowledged. sent is the
// number of bytes this call put on the wire; on failure the result subtracts
// what is still unacknowledged. Cancellation fails the stream because the
// outstanding acknowledgements become unmatchable.
func (s *Stream) awaitInputAcks(ctx context.Context, sent int) (int, error) {
	for {
		s.mu.Lock()
		unacked := min(s.inflightBytes, sent)
		if s.inputErr != nil {
			err := s.inputErr
			unacked = min(s.inputErrUnacked, sent)
			s.mu.Unlock()
			return sent - unacked, err
		}
		if len(s.inflight) == 0 {
			s.mu.Unlock()
			return sent, nil
		}
		if err := s.inputStateErrorLocked(); err != nil && !errors.Is(err, ErrExited) {
			s.mu.Unlock()
			return sent - unacked, err
		}
		wake := s.inputWake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			s.fail(context.Cause(ctx))
			return sent - unacked, context.Cause(ctx)
		case <-s.closed:
			// The connection is gone with frames still unacknowledged (for
			// example the session exited and the supervisor hung up).
			s.mu.Lock()
			err := s.readErr
			s.mu.Unlock()
			if err == nil {
				err = base.ErrDisconnected
			}
			return sent - unacked, err
		}
	}
}

// inputStateError reports why input can no longer be accepted, or nil.
func (s *Stream) inputStateError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputStateErrorLocked()
}

func (s *Stream) inputStateErrorLocked() error {
	switch {
	case s.userClosed:
		return ErrClosed
	case s.inputErr != nil:
		return s.inputErr
	case s.readErr != nil:
		return s.readErr
	case s.dead:
		return fmt.Errorf("%w: %s", ErrExited, s.info.ID)
	}
	select {
	case <-s.closed:
		return base.ErrDisconnected
	default:
		return nil
	}
}

// reserveInput waits for window space and records the frame as in flight.
func (s *Stream) reserveInput(ctx context.Context, size int) error {
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		s.mu.Lock()
		if err := s.inputStateErrorLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
		if len(s.inflight) == 0 || (s.inflightBytes+size <= inputWindowBytes && len(s.inflight) < inputWindowFrames) {
			s.inflight = append(s.inflight, size)
			s.inflightBytes += size
			s.mu.Unlock()
			return nil
		}
		wake := s.inputWake
		s.mu.Unlock()
		if s.windowWaitHook != nil {
			s.windowWaitHook()
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-s.closed:
		}
	}
}

// unreserveInput withdraws the newest reservation. Only the token holder
// appends, and nothing was sent for it, so no acknowledgement can refer to it.
func (s *Stream) unreserveInput(size int) {
	s.mu.Lock()
	s.inflight = s.inflight[:len(s.inflight)-1]
	s.inflightBytes -= size
	s.wakeInputLocked()
	s.mu.Unlock()
}

func (s *Stream) wakeInputLocked() {
	close(s.inputWake)
	s.inputWake = make(chan struct{})
}

func (s *Stream) sendInput(ctx context.Context, chunk []byte) error {
	if err := ctx.Err(); err != nil {
		s.unreserveInput(len(chunk))
		return context.Cause(ctx)
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		s.fail(context.Cause(ctx))
		close(cancelDone)
	})
	payload, err := inputPayload(s.identity.Generation, s.identity.Incarnation, chunk)
	if err == nil {
		s.writeMu.Lock()
		err = writeFrame(s.conn.conn, frameInput, payload)
		s.writeMu.Unlock()
	}
	if !stopCancel() {
		<-cancelDone
		return context.Cause(ctx)
	}
	if err != nil {
		s.mu.Lock()
		readErr := s.readErr
		s.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("%w: send frame: %v", base.ErrDisconnected, err)
	}
	return nil
}

// handleInputAck retires the oldest in-flight frame (readLoop only).
func (s *Stream) handleInputAck(payload []byte) error {
	var ack inputAckMsg
	if err := unmarshalFrame(payload, &ack); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inflight) == 0 {
		return fmt.Errorf("%w: unsolicited input acknowledgement", ErrProtocol)
	}
	size := s.inflight[0]
	s.inflight = s.inflight[1:]
	s.inflightBytes -= size
	switch {
	case ack.Error != nil:
		if s.inputErr == nil {
			s.inputErr = decodedError(*ack.Error)
			s.inputErrUnacked = s.inflightBytes
		}
	case ack.Written != size:
		return fmt.Errorf("%w: input acknowledgement of %d bytes for a %d-byte frame", ErrProtocol, ack.Written, size)
	}
	s.wakeInputLocked()
	return nil
}

// FlushInput waits until every accepted input frame has been acknowledged by
// the supervisor and returns the first rejection, if any.
func (s *Stream) FlushInput(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.inputErr != nil {
			err := s.inputErr
			s.mu.Unlock()
			return err
		}
		if len(s.inflight) == 0 {
			s.mu.Unlock()
			return nil
		}
		if s.readErr != nil {
			err := s.readErr
			s.mu.Unlock()
			return err
		}
		wake := s.inputWake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-s.closed:
			if err := s.inputStateError(); err != nil && !errors.Is(err, ErrClosed) {
				return err
			}
			return base.ErrDisconnected
		}
	}
}

// retireLocked records consumed (or skipped) output and wakes the credit
// sender once a full step has accumulated. s.mu must be held.
func (s *Stream) retireLocked(count uint64) {
	if count == 0 {
		return
	}
	s.retired += count
	if s.retired-s.creditSent >= outputCreditStep {
		select {
		case s.creditWake <- struct{}{}:
		default:
		}
	}
}

// creditLoop returns output credit off the reader's path, so a consumer never
// blocks on a socket write. Credit is cumulative, which makes a coalesced or
// repeated frame harmless.
func (s *Stream) creditLoop() {
	var payload [8]byte
	for {
		select {
		case <-s.creditWake:
		case <-s.closed:
			return
		}
		s.mu.Lock()
		retired := s.retired
		pending := retired > s.creditSent
		if pending {
			s.creditSent = retired
		}
		s.mu.Unlock()
		if !pending {
			continue
		}
		binary.BigEndian.PutUint64(payload[:], retired)
		s.writeMu.Lock()
		err := writeFrame(s.conn.conn, frameCredit, payload[:])
		s.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *Stream) Resize(ctx context.Context, cols, rows uint32) error {
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	kind, _, err := s.request(ctx, frameResize, resizeMsg{Cols: cols, Rows: rows, ExpectGeneration: s.identity.Generation, ExpectIncarnation: s.identity.Incarnation})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

// MarkStateSeen tells the helper that the user saw the needs_me or
// just_finished state that began at since; the helper pushes the seen state
// to every attached client.
func (s *Stream) MarkStateSeen(ctx context.Context, since time.Time) error {
	kind, _, err := s.request(ctx, frameStateSeen, stateSeenMsg{Since: since})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	return nil
}

// TapPrompt answers the session's detected prompt with option when the
// prompt identified by epoch and fingerprint is still shown and tappable,
// and returns the bytes the helper wrote. It first waits for the stream's
// earlier input to be acknowledged, so the keys follow it. A stale prompt
// fails with ErrStalePrompt.
func (s *Stream) TapPrompt(ctx context.Context, epoch uint64, fingerprint string, option int) (string, error) {
	if err := s.FlushInput(ctx); err != nil {
		return "", err
	}
	kind, payload, err := s.request(ctx, frameTapPrompt, tapPromptMsg{
		ExpectGeneration: s.identity.Generation, ExpectIncarnation: s.identity.Incarnation,
		Epoch: epoch, Fingerprint: fingerprint, Option: option,
	})
	if err != nil {
		return "", err
	}
	if kind != frameOK {
		return "", protocolMismatch(kind)
	}
	var reply tapPromptReply
	if err := unmarshalFrame(payload, &reply); err != nil {
		return "", err
	}
	return reply.Sent, nil
}

func (s *Stream) Kill(ctx context.Context) error {
	kind, _, err := s.request(ctx, frameKill, struct{}{})
	if err != nil {
		return err
	}
	if kind != frameOK {
		return protocolMismatch(kind)
	}
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
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
		s.wakeInputLocked()
		s.mu.Unlock()
		select {
		case s.reqToken <- struct{}{}:
		case <-time.After(s.closeDrainTimeout):
			_ = s.conn.Close()
			s.reqToken <- struct{}{}
		}
		defer func() { <-s.reqToken }()
		// Input accepted into the window must reach the supervisor before the
		// detach; a stalled supervisor is cut off after the drain timeout.
		var droppedErr error
		flushCtx, cancel := context.WithTimeout(context.Background(), s.closeDrainTimeout)
		inputErr := s.FlushInput(flushCtx)
		cancel()
		if errors.Is(inputErr, context.DeadlineExceeded) {
			inputErr = nil
			s.mu.Lock()
			dropped := len(s.inflight)
			s.mu.Unlock()
			droppedErr = fmt.Errorf("%w: %d unacknowledged input frames dropped", base.ErrDisconnected, dropped)
			_ = s.conn.Close()
		}
		s.mu.Lock()
		readErr := s.readErr
		killed := s.killed
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
		case droppedErr != nil:
			s.closeErr = droppedErr
		case inputErr != nil && !killed && !errors.Is(inputErr, base.ErrDisconnected) && !errors.Is(inputErr, ErrExited):
			// Input rejected because the session ended is not a close failure:
			// the exit itself is reported by Wait and by later writes.
			s.closeErr = inputErr
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
		if kind == frameInputAck {
			if ackErr := s.handleInputAck(payload); ackErr != nil {
				s.fail(ackErr)
				return
			}
			continue
		}
		switch kind {
		case frameOutput, frameReplayGzip, frameOutputZstd:
			var seq uint64
			var data []byte
			if kind != frameOutput && !s.conn.compress {
				s.fail(fmt.Errorf("%w: compressed output the client did not ask for", ErrProtocol))
				return
			}
			if kind == frameOutputZstd {
				seq, data, err = parseOutputZstd(payload)
			} else if kind == frameReplayGzip {
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
				s.retireLocked(drop)
			}
			// A credited server never runs further ahead than the window.
			if len(s.readBuf)+len(data) > outputCreditWindow+2*replayChunkSize {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: stream buffer overflow", ErrProtocol))
				return
			}
			s.readBuf = append(s.readBuf, data...)
			s.buffered += uint64(len(data))
			s.peakBuffered = max(s.peakBuffered, len(s.readBuf))
			s.mu.Unlock()
			select {
			case s.newData <- struct{}{}:
			default:
			}
		case frameHistoryGap:
			from, to, err := parseGap(payload)
			if err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			if !s.started {
				s.started = true
				s.seq = from
			}
			if from != s.seq || from < s.want {
				s.mu.Unlock()
				s.fail(fmt.Errorf("%w: history gap at %d does not match expected %d", ErrProtocol, from, s.seq))
				return
			}
			s.seq = to
			s.gaps++
			s.gapMarks = append(s.gapMarks, streamGap{at: s.buffered, to: to})
			// The server counts the dropped range as streamed.
			s.retireLocked(to - from)
			s.mu.Unlock()
			select {
			case s.newData <- struct{}{}:
			default:
			}
		case frameState:
			var msg stateMsg
			if err := unmarshalFrame(payload, &msg); err != nil {
				s.fail(err)
				return
			}
			s.mu.Lock()
			s.applyStateLocked(msg.State)
			s.mu.Unlock()
			select {
			case s.stateWake <- struct{}{}:
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
