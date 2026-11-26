// Package rstream is a transport-agnostic resumable byte stream. A Stream is one logical, ordered, exactly-once
// byte stream in each direction that runs over a carrier (any net.Conn: a relayed websocket bridge today, a QUIC
// stream later) and survives a carrier switch or loss: each direction counts 64-bit byte offsets, the sender
// keeps unacknowledged bytes in a bounded buffer, and a RESUME on a new carrier re-sends from the offset the
// peer reports.
//
// The client opens a stream with Open and moves it with Migrate (make-before-break) or after a loss; the agent
// side accepts carriers with Server.Accept, which yields a new stream for an OPEN and re-attaches a retained one
// for a RESUME.
package rstream

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	// DefaultBufferSize is the unacknowledged send buffer of a stream.
	DefaultBufferSize = 1 << 20
	// TerminalBufferSize is the send buffer of a terminal stream: input and output are small and latency bound.
	TerminalBufferSize = 256 * 1024
	// DefaultRetain is how long the agent keeps a stream that lost its carrier.
	DefaultRetain = 30 * time.Second
	// DefaultResumeTimeout is how long a client stream waits for a replacement carrier; it is shorter than the
	// agent's retention so the client gives up first and its session reconnect takes over.
	DefaultResumeTimeout = 20 * time.Second
	// DefaultLinger bounds how long a closed stream waits for the peer to finish.
	DefaultLinger = 30 * time.Second

	minWindow = 4 * 1024
	maxAckGap = 256 * 1024
	ackDelay  = 50 * time.Millisecond
	readerBuf = 64 * 1024
)

// PurposeTerminal is the terminal purpose; an empty purpose means the same.
const PurposeTerminal = "terminal"

var (
	// ErrCarrierLost ends a stream whose carrier was lost and not replaced in time.
	ErrCarrierLost = errors.New("rstream: carrier lost")
	// ErrUnknownStream is the agent's answer to a RESUME for a stream it no longer holds.
	ErrUnknownStream = errors.New("rstream: unknown stream")

	errFinished = errors.New("rstream: stream finished")
)

func normalizePurpose(purpose string) string {
	if purpose == "" {
		return PurposeTerminal
	}
	return purpose
}

type role uint8

const (
	roleClient role = iota
	roleAgent
)

type carrier struct {
	conn   net.Conn
	reader *bufio.Reader
}

// Stream is one resumable byte stream; it implements net.Conn.
type Stream struct {
	role     role
	sid      [sidSize]byte
	key      [keySize]byte
	purpose  string
	window   int
	ackEvery int
	budget   *Budget
	clock    Clock
	// lostTimeout is the client's resume window or the agent's retention.
	lostTimeout time.Duration
	linger      time.Duration
	server      *Server
	local       net.Addr
	remote      net.Addr

	migrateMu sync.Mutex

	mu         sync.Mutex
	wake       chan struct{}
	closeQueue []net.Conn

	carrier     *carrier
	resumeGen   uint64
	migrating   bool
	onLost      func()
	lostPending bool
	lostTimer   Timer
	lostSeq     uint64

	send         ring
	acked        uint64
	written      uint64
	sendNext     uint64
	held         int
	closing      bool
	finSent      bool
	finDelivered bool

	recv        []byte
	received    uint64
	consumed    uint64
	peerFin     bool
	ackConsumed uint64
	ackReceived uint64
	// ackFlushed is the receive position the peer was told in a completed carrier write (or a RESUME it answered).
	ackFlushed uint64
	needAck    bool
	ackTimer   *time.Timer
	resumeOK   bool
	resetOwed  bool
	resetCode  uint16
	resetText  string

	localClosed bool
	err         error
	done        chan struct{}
	lingerTimer Timer

	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer
	writeTimer    *time.Timer
}

func newStream(r role, purpose string, window int, budget *Budget, clock Clock, lostTimeout, linger time.Duration) *Stream {
	return &Stream{
		role: r, purpose: purpose, window: window, ackEvery: min(maxAckGap, window/4),
		budget: budget, clock: clock, lostTimeout: lostTimeout, linger: linger,
		wake: make(chan struct{}), done: make(chan struct{}),
	}
}

// attachFirst installs the carrier a stream was opened or accepted on and starts the stream's goroutines.
func (s *Stream) attachFirst(c *carrier) {
	s.local, s.remote = c.conn.LocalAddr(), c.conn.RemoteAddr()
	s.carrier = c
	go s.writeLoop()
	go s.readLoop(c)
}

// unlock releases s.mu and closes the carriers queued meanwhile; closing a websocket carrier performs a close
// handshake, so it never runs under the lock or on the caller's goroutine.
func (s *Stream) unlock() {
	queue := s.closeQueue
	s.closeQueue = nil
	s.mu.Unlock()
	for _, conn := range queue {
		go func() { _ = conn.Close() }()
	}
}

func (s *Stream) broadcastLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// waitLocked waits for a state change or extra; s.mu is released meanwhile.
func (s *Stream) waitLocked(extra <-chan struct{}) {
	wake := s.wake
	s.mu.Unlock()
	select {
	case <-wake:
	case <-extra:
	}
	s.mu.Lock()
}

// receivedWire is the receive position on the wire: received data plus one for a received FIN.
func (s *Stream) receivedWire() uint64 {
	if s.peerFin {
		return s.received + 1
	}
	return s.received
}

// attachLocked switches the stream to c after the peer reported peerRecv as its receive position.
func (s *Stream) attachLocked(c *carrier, peerRecv uint64) error {
	if s.err != nil {
		return s.err
	}
	limit := s.written
	if s.closing {
		limit++
	}
	if peerRecv < s.acked || peerRecv > limit {
		return errProtocol
	}
	if old := s.carrier; old != nil {
		s.closeQueue = append(s.closeQueue, old.conn)
	}
	s.carrier = c
	s.sendNext = min(peerRecv, s.written)
	s.finSent = false
	if s.closing && peerRecv == s.written+1 {
		s.finDelivered = true
	}
	s.needAck = true
	s.lostPending = false
	s.lostSeq++
	if s.lostTimer != nil {
		s.lostTimer.Stop()
		s.lostTimer = nil
	}
	s.broadcastLocked()
	go s.readLoop(c)
	return nil
}

func (s *Stream) carrierFailedLocked(c *carrier) {
	if s.carrier != c {
		return
	}
	s.carrier = nil
	s.closeQueue = append(s.closeQueue, c.conn)
	if s.err != nil || s.migrating {
		return
	}
	s.startLostLocked()
}

// startLostLocked begins the wait for a replacement carrier: the client runs its hook, the agent retains.
func (s *Stream) startLostLocked() {
	if s.lostPending || s.err != nil {
		return
	}
	if s.closing && s.finDelivered && s.peerFin {
		// Every byte and both FINs were delivered; only the peer's view of our final ACK may be missing, and a
		// peer that closed the carrier had it. Resuming would find the peer finished, so finish here.
		s.failLocked(errFinished, 0)
		return
	}
	if s.role == roleClient && s.onLost == nil {
		s.failLocked(ErrCarrierLost, 0)
		return
	}
	s.lostPending = true
	s.lostSeq++
	seq := s.lostSeq
	s.lostTimer = s.clock.AfterFunc(s.lostTimeout, func() {
		s.mu.Lock()
		if s.lostSeq == seq && s.carrier == nil {
			s.failLocked(ErrCarrierLost, 0)
		}
		s.unlock()
	})
	if s.role == roleClient {
		go s.onLost()
	}
}

// failLocked ends the stream with err. A non-zero reset code tells the peer why over the current carrier.
func (s *Stream) failLocked(err error, resetCode uint16) {
	if s.err != nil {
		return
	}
	s.err = err
	close(s.done)
	s.budget.release(s.held)
	s.held = 0
	s.send = ring{}
	for _, timer := range []Timer{s.lostTimer, s.lingerTimer} {
		if timer != nil {
			timer.Stop()
		}
	}
	if s.ackTimer != nil {
		s.ackTimer.Stop()
	}
	if s.carrier != nil {
		if resetCode != 0 {
			s.resetOwed, s.resetCode, s.resetText = true, resetCode, err.Error()
		} else {
			s.closeQueue = append(s.closeQueue, s.carrier.conn)
			s.carrier = nil
		}
	}
	if s.server != nil {
		s.server.remove(s)
	}
	s.broadcastLocked()
}

// maybeFinishLocked ends the stream once both directions are closed and acknowledged: this side's FIN reached
// the peer and the peer's FIN was received and reported back.
func (s *Stream) maybeFinishLocked() {
	if s.err == nil && s.closing && s.finDelivered && s.peerFin && s.ackFlushed == s.receivedWire() {
		s.failLocked(errFinished, 0)
	}
}

func (s *Stream) scheduleAckLocked() {
	if s.err != nil {
		return
	}
	if s.consumed-s.ackConsumed >= uint64(s.ackEvery) {
		s.needAck = true
		s.broadcastLocked()
		return
	}
	if s.ackTimer != nil {
		return
	}
	s.ackTimer = time.AfterFunc(ackDelay, func() {
		s.mu.Lock()
		s.ackTimer = nil
		if s.err == nil && (s.consumed != s.ackConsumed || s.receivedWire() != s.ackReceived) {
			s.needAck = true
			s.broadcastLocked()
		}
		s.unlock()
	})
}

func (s *Stream) writerReadyLocked() bool {
	if s.carrier == nil {
		return false
	}
	if s.resetOwed {
		return true
	}
	if s.err != nil {
		return false
	}
	return s.resumeOK || s.needAck || s.sendNext < s.written ||
		(s.closing && !s.finSent && !s.finDelivered && s.sendNext == s.written)
}

func (s *Stream) buildBatchLocked(batch []byte) []byte {
	if s.resetOwed {
		return appendReset(batch, s.resetCode, s.resetText)
	}
	if s.resumeOK {
		s.resumeOK = false
		s.ackReceived = s.receivedWire()
		batch = appendOffset(batch, frameResumeOK, s.ackReceived)
	}
	if s.needAck {
		s.needAck = false
		s.ackConsumed, s.ackReceived = s.consumed, s.receivedWire()
		batch = appendAck(batch, s.ackConsumed, s.ackReceived)
	}
	for sent := 0; s.sendNext < s.written && sent < batchLimit; {
		n := int(min(uint64(dataChunk), s.written-s.sendNext, uint64(batchLimit-sent)))
		batch = appendDataHeader(batch, s.sendNext, n)
		start := len(batch)
		batch = append(batch, make([]byte, n)...)
		s.send.copyAt(batch[start:], int(s.sendNext-s.acked))
		s.sendNext += uint64(n)
		sent += n
	}
	if s.closing && !s.finSent && !s.finDelivered && s.sendNext == s.written {
		s.finSent = true
		batch = appendOffset(batch, frameFin, s.written)
	}
	return batch
}

// writeLoop is the only writer of the stream's carriers, so frames never interleave.
func (s *Stream) writeLoop() {
	var batch []byte
	s.mu.Lock()
	for {
		if !s.writerReadyLocked() {
			if s.err != nil && !s.resetOwed {
				s.unlock()
				return
			}
			s.waitLocked(nil)
			continue
		}
		c := s.carrier
		resetting := s.resetOwed
		batch = s.buildBatchLocked(batch[:0])
		reported := s.ackReceived
		s.mu.Unlock()
		_, err := c.conn.Write(batch)
		s.mu.Lock()
		if resetting {
			s.resetOwed = false
			if s.carrier == c {
				s.carrier = nil
			}
			s.closeQueue = append(s.closeQueue, c.conn)
			continue
		}
		if err != nil {
			s.carrierFailedLocked(c)
			continue
		}
		s.ackFlushed = max(s.ackFlushed, reported)
		s.maybeFinishLocked()
	}
}

func (s *Stream) readLoop(c *carrier) {
	for {
		kind, payload, err := readFrame(c.reader)
		s.mu.Lock()
		if s.carrier != c || s.err != nil {
			s.unlock()
			return
		}
		if err == nil {
			err = s.handleFrameLocked(kind, payload)
			if err == nil {
				s.unlock()
				continue
			}
			if reset, ok := errors.AsType[*ResetError](err); ok {
				s.failLocked(reset, 0)
			} else {
				s.failLocked(err, ResetProtocol)
			}
			s.unlock()
			return
		}
		s.carrierFailedLocked(c)
		s.unlock()
		return
	}
}

func (s *Stream) handleFrameLocked(kind byte, payload []byte) error {
	switch kind {
	case frameData:
		offset, data, err := parseData(payload)
		if err != nil {
			return err
		}
		end := offset + uint64(len(data))
		if end <= s.received {
			// A re-send of bytes already received, possibly after the FIN: a RESUME on the new carrier reported
			// an older position while the old carrier was still delivering.
			return nil
		}
		if s.peerFin || offset > s.received {
			return errProtocol
		}
		data = data[s.received-offset:]
		if s.localClosed {
			s.consumed += uint64(len(data))
		} else {
			if len(s.recv)+len(data) > s.window {
				return errProtocol
			}
			s.recv = append(s.recv, data...)
		}
		s.received = end
		s.scheduleAckLocked()
		s.broadcastLocked()
	case frameAck:
		consumed, received, err := parseAck(payload)
		if err != nil {
			return err
		}
		limit := s.written
		if s.closing {
			limit++
		}
		if consumed > s.written || received > limit || consumed > received {
			return errProtocol
		}
		if consumed > s.acked {
			n := int(consumed - s.acked)
			s.send.discard(n)
			s.acked = consumed
			s.held -= n
			s.budget.release(n)
			s.broadcastLocked()
		}
		if s.closing && received == s.written+1 {
			s.finDelivered = true
		}
		s.maybeFinishLocked()
	case frameFin:
		offset, err := parseOffset(payload)
		if err != nil {
			return err
		}
		if offset != s.received {
			return errProtocol
		}
		if !s.peerFin {
			s.peerFin = true
			s.needAck = true
			s.broadcastLocked()
		}
	case frameReset:
		reset, err := parseReset(payload)
		if err != nil {
			return err
		}
		return reset
	default:
		return errProtocol
	}
	return nil
}

// Read reads stream bytes. After the peer closed its side it returns io.EOF once the buffered bytes are read.
func (s *Stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.unlock()
	for {
		if s.localClosed {
			return 0, net.ErrClosed
		}
		if len(s.recv) > 0 {
			n := copy(p, s.recv)
			s.recv = s.recv[n:]
			if len(s.recv) == 0 {
				s.recv = nil
			}
			s.consumed += uint64(n)
			s.scheduleAckLocked()
			return n, nil
		}
		if s.peerFin {
			return 0, io.EOF
		}
		if s.err != nil {
			return 0, s.err
		}
		if expired(s.readDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		if len(p) == 0 {
			return 0, nil
		}
		s.waitLocked(nil)
	}
}

// Write buffers p for delivery. It blocks while the stream's unacknowledged bytes fill its buffer or the shared
// budget is spent; it returns once every byte is buffered, not when the peer received it.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.unlock()
	total := 0
	for len(p) > 0 {
		if s.localClosed || s.closing {
			return total, net.ErrClosed
		}
		if s.err != nil {
			return total, s.err
		}
		if expired(s.writeDeadline) {
			return total, os.ErrDeadlineExceeded
		}
		var budgetChanged <-chan struct{}
		if room := s.window - int(s.written-s.acked); room > 0 {
			budgetChanged = s.budget.changed()
			if grant := s.budget.acquire(min(room, len(p))); grant > 0 {
				s.send.write(p[:grant], s.window)
				s.written += uint64(grant)
				s.held += grant
				p = p[grant:]
				total += grant
				s.broadcastLocked()
				continue
			}
		}
		s.waitLocked(budgetChanged)
	}
	return total, nil
}

// Close closes this side: buffered bytes and a FIN still reach the peer, through later carriers if needed, until
// the peer closes too or the linger time ends. Read and Write fail from now on.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.unlock()
	if s.localClosed {
		return nil
	}
	s.localClosed = true
	if len(s.recv) > 0 {
		s.consumed += uint64(len(s.recv))
		s.recv = nil
		s.scheduleAckLocked()
	}
	if s.err != nil {
		return nil
	}
	s.closing = true
	s.lingerTimer = s.clock.AfterFunc(s.linger, func() {
		s.mu.Lock()
		s.failLocked(&ResetError{Code: ResetTimeout, Message: "peer did not finish the closed stream"}, ResetTimeout)
		s.unlock()
	})
	s.broadcastLocked()
	s.maybeFinishLocked()
	return nil
}

// CloseWrite closes the sending direction: the peer reads EOF after the buffered bytes, and this side keeps
// reading until the peer closes too.
func (s *Stream) CloseWrite() error {
	s.mu.Lock()
	defer s.unlock()
	if s.localClosed || s.closing {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	s.closing = true
	s.broadcastLocked()
	return nil
}

// Done is closed when the stream ended: finished, reset, or its carrier was lost for good.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Purpose is the purpose the stream was opened for.
func (s *Stream) Purpose() string { return s.purpose }

func (s *Stream) LocalAddr() net.Addr  { return s.local }
func (s *Stream) RemoteAddr() net.Addr { return s.remote }

func (s *Stream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

func (s *Stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.unlock()
	s.readDeadline = t
	s.readTimer = s.armDeadlineLocked(s.readTimer, t)
	return nil
}

func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.unlock()
	s.writeDeadline = t
	s.writeTimer = s.armDeadlineLocked(s.writeTimer, t)
	return nil
}

func (s *Stream) armDeadlineLocked(timer *time.Timer, t time.Time) *time.Timer {
	if timer != nil {
		timer.Stop()
		timer = nil
	}
	if !t.IsZero() {
		if wait := time.Until(t); wait > 0 {
			timer = time.AfterFunc(wait, func() {
				s.mu.Lock()
				s.broadcastLocked()
				s.unlock()
			})
		}
	}
	s.broadcastLocked()
	return timer
}

func expired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

var _ net.Conn = (*Stream)(nil)
