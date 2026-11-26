package rstream

import (
	"bufio"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ServerOptions configures the agent side.
type ServerOptions struct {
	// MaxBufferSize is the largest buffer a client may ask for; zero means DefaultBufferSize.
	MaxBufferSize int
	// Budget caps the unacknowledged send bytes of every accepted stream; nil creates one of DefaultBudgetBytes.
	Budget *Budget
	// Retain is how long a stream that lost its carrier waits for a RESUME.
	Retain time.Duration
	// Linger bounds how long a closed stream waits for the client to finish.
	Linger time.Duration
	// HandshakeTimeout bounds the wait for a carrier's first frame.
	HandshakeTimeout time.Duration
	// Clock schedules retention and linger; nil uses real time.
	Clock Clock
}

// Server holds the agent's streams so a RESUME on a new carrier finds the stream it continues.
type Server struct {
	opts ServerOptions

	mu      sync.Mutex
	streams map[[sidSize]byte]*Stream
	// finished remembers recently completed streams until the given time, so a client whose final ACK was lost
	// learns that its stream completed instead of that it is unknown.
	finished map[[sidSize]byte]time.Time
	closed   bool
}

const defaultHandshakeTimeout = 30 * time.Second

// NewServer returns an agent-side stream server.
func NewServer(opts ServerOptions) *Server {
	if opts.MaxBufferSize <= 0 {
		opts.MaxBufferSize = DefaultBufferSize
	}
	if opts.Budget == nil {
		opts.Budget = NewBudget(DefaultBudgetBytes)
	}
	if opts.Retain <= 0 {
		opts.Retain = DefaultRetain
	}
	if opts.Linger <= 0 {
		opts.Linger = DefaultLinger
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	return &Server{opts: opts, streams: make(map[[sidSize]byte]*Stream), finished: make(map[[sidSize]byte]time.Time)}
}

// Accept reads the first frame of carrier, which was authorized for carrierPurpose ("" means terminal).
//   - OPEN: it returns a new stream to hand to the purpose handler, with resumed false.
//   - RESUME: it re-attaches the retained stream the frame names and returns it with resumed true; the stream is
//     already being served, so the caller must not dispatch it again.
//
// A stream's purpose must equal its carrier's, for OPEN and RESUME alike, so a carrier authorized for one
// purpose never carries another's stream. A refused carrier gets a RESET and is closed; it never affects the
// stream it named. ctx bounds only the wait for the first frame; the carrier must outlive it. Accept owns
// carrier: on error it is closed.
func (srv *Server) Accept(ctx context.Context, conn net.Conn, carrierPurpose string) (net.Conn, bool, error) {
	carrierPurpose = normalizePurpose(carrierPurpose)
	ctx, cancel := context.WithTimeout(ctx, srv.opts.HandshakeTimeout)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	reader := bufio.NewReaderSize(conn, readerBuf)
	kind, payload, err := readFrame(reader)
	if !stop() {
		err = errors.Join(ctx.Err(), err)
	}
	cancel()
	if err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("rstream: read first frame: %w", err)
	}
	switch kind {
	case frameOpen:
		stream, err := srv.open(conn, reader, payload, carrierPurpose)
		if err != nil {
			return nil, false, err
		}
		return stream, false, nil
	case frameResume:
		stream, err := srv.resume(conn, reader, payload, carrierPurpose)
		if err != nil {
			return nil, false, err
		}
		return stream, true, nil
	default:
		return nil, false, refuse(conn, ResetProtocol, fmt.Sprintf("unexpected first frame %d", kind))
	}
}

// refuse answers a carrier with RESET and closes it.
func refuse(conn net.Conn, code uint16, message string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Write(appendReset(nil, code, message))
	_ = conn.Close()
	return &ResetError{Code: code, Message: message}
}

func (srv *Server) open(conn net.Conn, reader *bufio.Reader, payload []byte, carrierPurpose string) (*Stream, error) {
	open, err := parseOpen(payload)
	if err != nil {
		return nil, refuse(conn, ResetProtocol, err.Error())
	}
	if purpose := normalizePurpose(open.purpose); purpose != carrierPurpose {
		return nil, refuse(conn, ResetPurposeMismatch, fmt.Sprintf("stream purpose %q on a %q carrier", purpose, carrierPurpose))
	}
	if window := int(open.window); window < minWindow || window > srv.opts.MaxBufferSize {
		return nil, refuse(conn, ResetProtocol, fmt.Sprintf("buffer size %d out of range", window))
	}
	s := newStream(roleAgent, carrierPurpose, int(open.window), srv.opts.Budget, srv.opts.Clock, srv.opts.Retain, srv.opts.Linger)
	s.sid, s.key, s.server = open.sid, open.key, srv
	srv.mu.Lock()
	switch {
	case srv.closed:
		srv.mu.Unlock()
		return nil, refuse(conn, ResetShutdown, "agent is shutting down")
	case srv.streams[open.sid] != nil:
		srv.mu.Unlock()
		return nil, refuse(conn, ResetDuplicateStream, "stream id in use")
	}
	srv.streams[open.sid] = s
	srv.mu.Unlock()
	s.attachFirst(&carrier{conn: conn, reader: reader})
	return s, nil
}

func (srv *Server) resume(conn net.Conn, reader *bufio.Reader, payload []byte, carrierPurpose string) (*Stream, error) {
	resume, err := parseResume(payload)
	if err != nil {
		return nil, refuse(conn, ResetProtocol, err.Error())
	}
	srv.mu.Lock()
	s := srv.streams[resume.sid]
	_, finished := srv.finished[resume.sid]
	srv.mu.Unlock()
	if s == nil && finished {
		return nil, refuse(conn, ResetFinished, "stream finished")
	}
	if s == nil {
		return nil, refuse(conn, ResetUnknownStream, "no such stream")
	}
	// The purpose is checked before the MAC, but it reveals nothing: a carrier of the wrong purpose is refused
	// either way, and the stream is untouched.
	if s.purpose != carrierPurpose {
		return nil, refuse(conn, ResetPurposeMismatch, fmt.Sprintf("stream purpose %q on a %q carrier", s.purpose, carrierPurpose))
	}
	want := resumeMAC(s.key, s.sid, resume.gen, resume.recv)
	if !hmac.Equal(want[:], resume.mac[:]) {
		return nil, refuse(conn, ResetBadMAC, "resume authentication failed")
	}
	s.mu.Lock()
	if s.err != nil {
		s.unlock()
		return nil, refuse(conn, ResetUnknownStream, "stream ended")
	}
	if resume.gen <= s.resumeGen {
		s.unlock()
		return nil, refuse(conn, ResetStaleResume, "resume replayed")
	}
	if err := s.attachLocked(&carrier{conn: conn, reader: reader}, resume.recv); err != nil {
		s.unlock()
		return nil, refuse(conn, ResetProtocol, "resume offset out of range")
	}
	s.resumeGen = resume.gen
	s.resumeOK = true
	s.unlock()
	return s, nil
}

// remove forgets s; called with s.mu held once s ended.
func (srv *Server) remove(s *Stream) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.streams[s.sid] != s {
		return
	}
	delete(srv.streams, s.sid)
	now := time.Now()
	for sid, until := range srv.finished {
		if now.After(until) {
			delete(srv.finished, sid)
		}
	}
	if errors.Is(s.err, errFinished) {
		srv.finished[s.sid] = now.Add(srv.opts.Retain)
	}
}

// Len reports how many streams the server holds, retained ones included.
func (srv *Server) Len() int {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return len(srv.streams)
}

// Close resets every stream and refuses new ones.
func (srv *Server) Close() error {
	srv.mu.Lock()
	srv.closed = true
	streams := make([]*Stream, 0, len(srv.streams))
	for _, s := range srv.streams {
		streams = append(streams, s)
	}
	srv.mu.Unlock()
	for _, s := range streams {
		s.mu.Lock()
		s.failLocked(&ResetError{Code: ResetShutdown, Message: "agent is shutting down"}, ResetShutdown)
		s.unlock()
	}
	return nil
}
