package rstream

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"time"
)

// Options configures a client stream.
type Options struct {
	// BufferSize bounds the unacknowledged send bytes; zero selects DefaultBufferSize, or TerminalBufferSize for
	// the terminal purpose. The agent uses the same size for its direction.
	BufferSize int
	// Budget caps the unacknowledged bytes of every stream sharing it; nil shares a process-wide default.
	Budget *Budget
	// ResumeTimeout is how long the stream waits for a replacement carrier after a loss.
	ResumeTimeout time.Duration
	// Linger bounds how long a closed stream waits for the peer to finish.
	Linger time.Duration
	// Clock schedules the resume and linger deadlines; nil uses real time.
	Clock Clock
}

func bufferSizeFor(purpose string, size int) int {
	if size > 0 {
		return max(size, minWindow)
	}
	if purpose == PurposeTerminal {
		return TerminalBufferSize
	}
	return DefaultBufferSize
}

// Open starts a stream of purpose on carrier: it sends OPEN and returns at once, so the first bytes can follow
// in the same round trip. The agent refuses a purpose other than the one its carrier was authorized for with a
// RESET, which the first Read or Write reports. The stream owns carrier from here on.
func Open(ctx context.Context, conn net.Conn, purpose string, opts Options) (*Stream, error) {
	purpose = normalizePurpose(purpose)
	if len(purpose) > maxPurpose {
		_ = conn.Close()
		return nil, fmt.Errorf("rstream: purpose %q too long", purpose)
	}
	budget, clock := opts.Budget, opts.Clock
	if budget == nil {
		budget = defaultClientBudget
	}
	if clock == nil {
		clock = realClock{}
	}
	resumeTimeout, linger := opts.ResumeTimeout, opts.Linger
	if resumeTimeout <= 0 {
		resumeTimeout = DefaultResumeTimeout
	}
	if linger <= 0 {
		linger = DefaultLinger
	}
	s := newStream(roleClient, purpose, bufferSizeFor(purpose, opts.BufferSize), budget, clock, resumeTimeout, linger)
	_, _ = rand.Read(s.sid[:])
	_, _ = rand.Read(s.key[:])
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	_, err := conn.Write(appendOpen(nil, openFrame{sid: s.sid, key: s.key, window: uint32(s.window), purpose: purpose}))
	if !stop() {
		err = errors.Join(ctx.Err(), err)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("rstream: send OPEN: %w", err)
	}
	s.attachFirst(&carrier{conn: conn, reader: bufio.NewReaderSize(conn, readerBuf)})
	return s, nil
}

// OnCarrierLost sets the hook run, on its own goroutine, when the stream loses its carrier outside a Migrate;
// the hook is expected to obtain a replacement and Migrate onto it before the resume timeout. Without a hook a
// carrier loss ends the stream at once. ResumeWith installs a hook of its own, so a caller uses one or the other.
func (s *Stream) OnCarrierLost(hook func()) {
	s.mu.Lock()
	s.onLost = hook
	s.mu.Unlock()
}

// Migrate moves the stream to carrier, make-before-break: it sends RESUME on carrier while the current carrier
// keeps working, and once the agent answers RESUME_OK it switches both directions to carrier, re-sends what the
// agent has not received and closes the old carrier. It also resumes a stream that already lost its carrier.
// On error carrier is closed; a stream the agent no longer holds ends with ErrUnknownStream, any other failure
// leaves the stream on its current carrier (or waiting for one). Calls are serialized.
func (s *Stream) Migrate(ctx context.Context, conn net.Conn) error {
	return s.migrate(ctx, conn, false)
}

// errAttached is the outcome of a resume attempt that found the stream already on a carrier.
var errAttached = errors.New("rstream: stream already has a carrier")

// migrate is Migrate; onlyDetached gives up, closing conn, when the stream has a carrier by the time it is the
// stream's turn, so a resume after a loss never replaces a carrier a concurrent migration installed.
func (s *Stream) migrate(ctx context.Context, conn net.Conn, onlyDetached bool) error {
	if s.role != roleClient {
		_ = conn.Close()
		return errors.New("rstream: only the client migrates a stream")
	}
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		_ = conn.Close()
		return err
	}
	if onlyDetached && s.carrier != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return errAttached
	}
	s.migrating = true
	s.resumeGen++
	resume := resumeFrame{sid: s.sid, gen: s.resumeGen, recv: s.receivedWire()}
	resume.mac = resumeMAC(s.key, s.sid, resume.gen, resume.recv)
	s.mu.Unlock()

	reader, peerRecv, err := handshakeResume(ctx, conn, resume)

	s.mu.Lock()
	defer s.unlock()
	s.migrating = false
	if err == nil {
		if err = s.attachLocked(&carrier{conn: conn, reader: reader}, peerRecv); errors.Is(err, errProtocol) {
			s.failLocked(fmt.Errorf("%w: RESUME_OK offset out of range", errProtocol), ResetProtocol)
		}
	}
	if err == nil {
		// The agent answered the RESUME, so it knows the receive position the RESUME reported.
		s.ackFlushed = max(s.ackFlushed, resume.recv)
		s.maybeFinishLocked()
		return nil
	}
	s.closeQueue = append(s.closeQueue, conn)
	if reset, ok := errors.AsType[*ResetError](err); ok && reset.Code == ResetFinished && s.closing && s.peerFin {
		// The agent completed the stream, which needs our FIN; only its final ACK did not reach us.
		s.failLocked(errFinished, 0)
		return errFinished
	}
	if reset, ok := errors.AsType[*ResetError](err); ok && (reset.Code == ResetUnknownStream || reset.Code == ResetFinished) {
		err = fmt.Errorf("%w: %s", ErrUnknownStream, reset.Message)
		s.failLocked(err, 0)
		return err
	}
	if s.carrier == nil && s.err == nil {
		// The carrier broke while the migration was in flight.
		s.startLostLocked()
	}
	return err
}

func handshakeResume(ctx context.Context, conn net.Conn, resume resumeFrame) (*bufio.Reader, uint64, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	reader, peerRecv, err := func() (*bufio.Reader, uint64, error) {
		if _, err := conn.Write(appendResume(nil, resume)); err != nil {
			return nil, 0, err
		}
		reader := bufio.NewReaderSize(conn, readerBuf)
		kind, payload, err := readFrame(reader)
		if err != nil {
			return nil, 0, err
		}
		switch kind {
		case frameResumeOK:
			offset, err := parseOffset(payload)
			return reader, offset, err
		case frameReset:
			reset, err := parseReset(payload)
			if err != nil {
				return nil, 0, err
			}
			return nil, 0, reset
		default:
			return nil, 0, fmt.Errorf("%w: frame %d before RESUME_OK", errProtocol, kind)
		}
	}()
	if !stop() {
		// ctx ended and closed conn, whatever the handshake made of it.
		err = errors.Join(ctx.Err(), err)
	}
	return reader, peerRecv, err
}

// permanentError marks a redial failure that retrying cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps a redial error that retrying cannot fix (the server refused the request), so ResumeWith ends
// the stream at once instead of retrying until the resume timeout.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

const (
	resumeRetryMin = 100 * time.Millisecond
	resumeRetryMax = 2 * time.Second
)

// ResumeWith keeps the stream attached across carrier losses: after a loss it opens a replacement with redial
// and migrates onto it, retrying with backoff until the stream resumes or its resume timeout ends it. A
// Permanent redial error, or an agent that no longer holds the stream, ends the stream at once so the caller's
// own reconnect takes over. It installs the OnCarrierLost hook.
func (s *Stream) ResumeWith(redial func(ctx context.Context) (net.Conn, error)) {
	s.OnCarrierLost(func() { s.resumeLoop(redial) })
}

func (s *Stream) resumeLoop(redial func(ctx context.Context) (net.Conn, error)) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	delay := resumeRetryMin
	for {
		s.mu.Lock()
		attached := s.carrier != nil
		s.mu.Unlock()
		if attached {
			return
		}
		conn, err := redial(ctx)
		if err == nil {
			err = s.migrate(ctx, conn, true)
			if err == nil || errors.Is(err, ErrUnknownStream) || errors.Is(err, errAttached) || errors.Is(err, errFinished) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if _, ok := errors.AsType[*permanentError](err); ok {
			s.mu.Lock()
			s.failLocked(fmt.Errorf("%w: %w", ErrCarrierLost, err), 0)
			s.unlock()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		delay = min(2*delay, resumeRetryMax)
	}
}
