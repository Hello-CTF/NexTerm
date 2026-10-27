package supervisor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
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
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
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
		serverConn := &serverConn{server: s, conn: conn, done: make(chan struct{}), creditWake: make(chan struct{}, 1)}
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

	// compress records the client's hello preference for gzip replay batches
	// and zstd live output.
	compress bool
	// rulesOwner is the hello's state rule owner key; empty for none.
	rulesOwner string

	// done closes with the connection so goroutines parked on input or credit
	// stop without waiting for the peer.
	done chan struct{}

	// Windowed input: frames queue here and one goroutine feeds
	// the session, so a full session queue delays acknowledgements instead of
	// stalling the read loop or rejecting the frame.
	inputOnce   sync.Once
	inputFrames chan inputFrame
	inputBytes  atomic.Int64

	// Output credit: creditMu guards credited, the client's
	// cumulative retired byte count, and sent, what the server has streamed.
	creditMu   sync.Mutex
	credited   uint64
	sent       uint64
	creditWake chan struct{}

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
		c.replyError(frameError, versionRejection(hello.Version))
		return
	}
	if hello.StateDigest != c.server.stateDigest {
		c.replyError(frameError, errorMsg{Code: codeStateMismatch, Message: "supervisor state digest does not match this endpoint"})
		return
	}
	c.compress = hello.CompressOutput
	if hello.StateRulesOwner != "" && !validStateRulesOwner(hello.StateRulesOwner) {
		c.replyError(frameError, errorMsg{Code: codeInvalidInput, Message: "malformed state rule owner key"})
		return
	}
	c.rulesOwner = hello.StateRulesOwner
	activity, err := c.server.supervisor.Activity(context.Background())
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	ack := helloMsg{
		Version: ProtocolVersion, Generation: c.server.supervisor.generation, Runtime: c.server.runtimeMetadata, Activity: &activity,
		StateSeen: true, StateRules: &stateRulesAck{Revision: c.server.supervisor.stateRulesSeen(c.rulesOwner)}, PromptTap: true,
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
			c.handleList(payload)
		case frameRename:
			c.handleRename(payload)
		case frameAttach:
			if !c.handleAttach(payload) {
				return
			}
		case frameInput:
			if !c.handleInput(payload) {
				return
			}
		case frameCredit:
			if !c.handleCredit(payload) {
				return
			}
		case frameResize:
			c.handleResize(payload)
		case frameStateSeen:
			c.handleStateSeen(payload)
		case frameTapPrompt:
			c.handleTapPrompt(payload)
		case frameKill:
			c.handleKill()
		case frameGetVersions:
			c.handleGetVersions()
		case frameSetVersions:
			c.handleSetVersions(payload)
		case frameKillSession:
			c.handleKillSession(payload)
		case frameReadHistory:
			c.handleReadHistory(payload)
		case frameSetStateRules:
			c.handleSetStateRules(payload)
		case frameDetach:
			_ = c.reply(frameOK, struct{}{})
			return
		default:
			// A frame from a newer client is refused without ending the
			// connection, so the client keeps its session. Only request/reply
			// frames may rely on this refusal, because the client waits for
			// an answer. A new frame that has no reply must first be
			// advertised in the hello ack, so the client never sends it to a
			// helper that would answer with an unexpected error frame.
			c.replyError(frameError, errorMsg{Code: codeProtocol, Message: fmt.Sprintf("unexpected frame type %d", kind)})
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
	session, err := c.server.supervisor.Create(context.Background(), CreateOptions{
		ID:      msg.ID,
		Attempt: msg.Attempt,
		Command: msg.Command,
		Dir:     msg.Dir,
		Env:     msg.Env,
		Cols:    msg.Cols,
		Rows:    msg.Rows,
		Pty:     msg.Pty,

		HistoryLimit:    msg.HistoryLimit,
		StateRulesOwner: c.rulesOwner,
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

func (c *serverConn) handleList(payload []byte) {
	var options listOptions
	if len(payload) > 0 {
		if err := unmarshalFrame(payload, &options); err != nil {
			c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
			return
		}
	}
	infos, err := c.server.supervisor.listCanonicalWith(context.Background(), options)
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameListed, infos)
}

func (c *serverConn) handleRename(payload []byte) {
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
	options := AttachOptions{Offset: msg.Offset, Tail: msg.Tail, Retain: msg.Retain}
	attachment, err := c.server.supervisor.attachFrom(context.Background(), msg.ID, options, expected, msg.ExpectGeneration)
	if err != nil {
		c.replyOperationError(err)
		return true
	}
	c.sessionMu.Lock()
	c.attachment = attachment
	c.sessionMu.Unlock()
	info := attachment.session.canonicalInfo()
	if err := c.reply(frameAttached, attachedMsg{Info: info, Replay: attachment.Replay()}); err != nil {
		_ = attachment.Detach()
		return false
	}
	go c.streamOutput(attachment)
	if info.State != nil {
		c.server.wg.Add(1)
		go func() {
			defer c.server.wg.Done()
			c.streamState(attachment, info.State.LastEventSeq)
		}()
	}
	return true
}

// streamState pushes the attached session's state whenever it changes,
// with the events after the last pushed sequence. The attachment registers
// for changes before the attach reply snapshots the state, so no change
// between the two is missed.
func (c *serverConn) streamState(attachment *Attachment, after uint64) {
	for {
		select {
		case <-attachment.StateUpdates():
		case <-attachment.ctx.Done():
			return
		case <-c.done:
			return
		}
		state := attachment.SessionState(after)
		if state == nil {
			continue
		}
		if c.reply(frameState, stateMsg{ID: attachment.ID(), State: *state}) != nil {
			return
		}
		after = state.LastEventSeq
	}
}

func (c *serverConn) handleInput(payload []byte) bool {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "input frame without attach"})
		return true
	}
	generation, incarnation, data, err := parseInput(payload)
	if err != nil {
		c.replyError(frameFatal, errorMsg{Code: codeProtocol, Message: err.Error()})
		return false
	}
	return c.queueWindowedInput(attachment, inputFrame{generation: generation, incarnation: incarnation, data: data})
}

// inputFrame is one parsed frameInput: the data and the helper generation
// and session incarnation the client meant it for.
type inputFrame struct {
	generation  string
	incarnation string
	data        []byte
}

// checkAttached rejects a request whose expectations name another helper
// process or another incarnation of the attached session. Both expectations
// are required.
func checkAttached(supervisor *Supervisor, attachment *Attachment, generation, incarnation string) error {
	if generation == "" {
		return fmt.Errorf("%w: request lacks the expected helper generation", ErrProtocol)
	}
	if err := supervisor.checkGeneration(generation); err != nil {
		return err
	}
	if current := attachment.session.Identity().Incarnation; incarnation != current {
		return fmt.Errorf("%w: request for incarnation %q, attached session is %q", ErrIdentity, incarnation, current)
	}
	return nil
}

// queueWindowedInput hands the frame to the input goroutine. The client bounds
// its own window; a peer that exceeds the server's tolerance is cut off rather
// than allowed to grow memory.
func (c *serverConn) queueWindowedInput(attachment *Attachment, frame inputFrame) bool {
	c.inputOnce.Do(func() {
		c.inputFrames = make(chan inputFrame, serverInputMaxFrames)
		c.server.wg.Add(1)
		go func() {
			defer c.server.wg.Done()
			c.pumpWindowedInput(attachment)
		}()
	})
	if c.inputBytes.Add(int64(len(frame.data))) > serverInputMaxBytes {
		c.replyError(frameFatal, errorMsg{Code: codeProtocol, Message: "input window exceeded"})
		return false
	}
	select {
	case c.inputFrames <- frame:
		return true
	default:
		c.replyError(frameFatal, errorMsg{Code: codeProtocol, Message: "input window exceeded"})
		return false
	}
}

// pumpWindowedInput feeds queued frames to the session in order and answers
// each with exactly one acknowledgement, which is how the client's window
// reopens. A blocked session write therefore throttles the client.
func (c *serverConn) pumpWindowedInput(attachment *Attachment) {
	for {
		select {
		case frame := <-c.inputFrames:
			var count int
			err := checkAttached(c.server.supervisor, attachment, frame.generation, frame.incarnation)
			if err == nil {
				count, err = attachment.writeBlocking(frame.data)
			}
			c.inputBytes.Add(-int64(len(frame.data)))
			ack := inputAckMsg{Written: count}
			if err != nil {
				msg := errorMessage(err)
				ack.Error = &msg
			}
			if c.reply(frameInputAck, ack) != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *serverConn) handleCredit(payload []byte) bool {
	if len(payload) != 8 {
		c.replyError(frameFatal, errorMsg{Code: codeProtocol, Message: "unexpected credit frame"})
		return false
	}
	retired := binary.BigEndian.Uint64(payload)
	c.creditMu.Lock()
	if retired > c.sent {
		c.creditMu.Unlock()
		c.replyError(frameFatal, errorMsg{Code: codeProtocol, Message: "credit exceeds streamed output"})
		return false
	}
	if retired > c.credited {
		c.credited = retired
	}
	c.creditMu.Unlock()
	select {
	case c.creditWake <- struct{}{}:
	default:
	}
	return true
}

// awaitCredit blocks while the client holds outputCreditWindow unretired
// bytes. Nothing is read from the recording meanwhile, so the data stays on
// disk and streaming resumes at the same offset.
func (c *serverConn) awaitCredit(attachment *Attachment) bool {
	for {
		c.creditMu.Lock()
		open := c.sent < c.credited+outputCreditWindow
		c.creditMu.Unlock()
		if open {
			return true
		}
		select {
		case <-c.creditWake:
		case <-attachment.ctx.Done():
			return false
		case <-c.done:
			return false
		}
	}
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

func (c *serverConn) handleReadHistory(payload []byte) {
	if c.attached() != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "history read on attached connection"})
		return
	}
	var msg readHistoryMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if msg.ExpectIncarnation == "" {
		c.replyError(frameError, errorMsg{Code: codeInvalidInput, Message: "history read requires the session identity"})
		return
	}
	expected := Identity{CreatedAt: time.Unix(0, msg.ExpectCreatedAt), Incarnation: msg.ExpectIncarnation}
	chunk, err := c.server.supervisor.readHistory(context.Background(), msg.ID, msg.Before, msg.MaxBytes, expected, msg.ExpectGeneration)
	if err != nil {
		c.replyOperationError(err)
		return
	}
	reply, err := historyPayload(chunk)
	if err != nil {
		c.replyOperationError(err)
		return
	}
	_ = c.replyRaw(frameHistory, reply)
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
	if err := checkAttached(c.server.supervisor, attachment, msg.ExpectGeneration, msg.ExpectIncarnation); err != nil {
		c.replyError(frameError, errorMessage(err))
		return
	}
	if err := attachment.Resize(context.Background(), msg.Cols, msg.Rows); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

func (c *serverConn) handleStateSeen(payload []byte) {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "state seen frame without attach"})
		return
	}
	var msg stateSeenMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := attachment.MarkStateSeen(msg.Since); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, struct{}{})
}

// handleTapPrompt answers the attached session's prompt. It runs on the
// frame loop: the client sends it only after its earlier input frames were
// acknowledged, so the keys follow that input.
func (c *serverConn) handleTapPrompt(payload []byte) {
	attachment := c.attached()
	if attachment == nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: "tap frame without attach"})
		return
	}
	var msg tapPromptMsg
	if err := unmarshalFrame(payload, &msg); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if err := checkAttached(c.server.supervisor, attachment, msg.ExpectGeneration, msg.ExpectIncarnation); err != nil {
		c.replyError(frameError, errorMessage(err))
		return
	}
	sent, err := attachment.TapPrompt(msg.Epoch, msg.Fingerprint, msg.Option)
	if err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
		return
	}
	_ = c.reply(frameOK, tapPromptReply{Sent: sent})
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

func (c *serverConn) handleSetStateRules(payload []byte) {
	var set StateRuleSet
	if err := unmarshalFrame(payload, &set); err != nil {
		c.replyError(frameError, errorMsg{Code: codeProtocol, Message: err.Error()})
		return
	}
	if c.rulesOwner == "" {
		c.replyError(frameError, errorMsg{Code: codeInvalidInput, Message: "state rules need a rule owner in the hello"})
		return
	}
	if err := c.server.supervisor.SetStateRules(c.rulesOwner, set); err != nil {
		c.replyError(frameError, errorMsg{Code: errorCode(err), Message: err.Error()})
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
	if c.compress {
		replayEnd = attachment.replayEnd
	}
	var compressor replayCompressor
	buffer := make([]byte, replayChunkSize)
	base := attachment.NextSeq()
	expected := base
	for {
		if !c.awaitCredit(attachment) {
			// A kill or detach cancels the attachment while parked on credit; deliver
			// the exit exactly as a Read that reports ErrClosed does.
			select {
			case <-c.done:
			default:
				if info := attachment.Info(); info.Dead {
					_ = c.reply(frameExit, exitMsg{Code: info.ExitCode, Signal: info.Signal})
				}
			}
			return
		}
		size := outputChunkSize
		if expected < replayEnd {
			size = int(min(uint64(replayChunkSize), replayEnd-expected))
		}
		seq, count, err := attachment.readChunk(buffer[:size])
		if count > 0 {
			if seq != expected {
				// The reader fell behind the history limit. The skipped
				// output counts as streamed so credit stays aligned.
				c.creditMu.Lock()
				c.sent = seq - base
				c.creditMu.Unlock()
				if writeErr := c.replyRaw(frameHistoryGap, gapPayload(expected, seq)); writeErr != nil {
					return
				}
			}
			kind, payload := frameOutput, outputPayload(seq, buffer[:count])
			if seq < replayEnd && count >= 1024 {
				if compressed, compressErr := compressor.encode(seq, buffer[:count]); compressErr == nil && len(compressed) < len(payload) {
					kind, payload = frameReplayGzip, compressed
				}
			}
			if c.compress && kind == frameOutput {
				// Credit below counts decoded bytes (seq advances by count), so
				// compression never widens the flow-control window.
				if packed, packErr := encodeOutputZstd(seq, buffer[:count]); packErr == nil && packed != nil {
					kind, payload = frameOutputZstd, packed
				}
			}
			expected = seq + uint64(count)
			// Counted before the write: the client may return credit for this
			// chunk before the write call comes back here.
			c.creditMu.Lock()
			c.sent = expected - base
			c.creditMu.Unlock()
			if writeErr := c.replyRaw(kind, payload); writeErr != nil {
				return
			}
			if expected >= replayEnd {
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
	c.replyError(frameError, errorMessage(err))
}

// versionRejection answers a hello of another protocol version. It names the
// side that must upgrade and carries this helper's version, so a newer client
// can tell its own upgrade apart from the helper's.
func versionRejection(clientVersion int) errorMsg {
	message := fmt.Sprintf("client protocol %d is older than helper protocol %d; update the client", clientVersion, ProtocolVersion)
	if clientVersion > ProtocolVersion {
		message = fmt.Sprintf("helper protocol %d is older than client protocol %d; the helper needs upgrade", ProtocolVersion, clientVersion)
	}
	return errorMsg{Code: codeVersionMismatch, Message: message, Version: ProtocolVersion}
}

func (c *serverConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
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
