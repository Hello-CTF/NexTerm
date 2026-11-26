package relaymux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Link is an authenticated Session together with its control stream. It is a Session itself; OpenDeviceStream
// and AcceptHeader add the per-stream header exchange on top of it.
type Link struct {
	Session
	control net.Conn
	reply   Reply

	mu  sync.Mutex
	err error
}

func newLink(session Session, control net.Conn, reply Reply) *Link {
	link := &Link{Session: session, control: control, reply: reply}
	go link.watchControl()
	go func() {
		<-session.Done()
		_ = control.Close()
	}()
	return link
}

// watchControl reads the control stream for the lifetime of the session: a goaway Reply records why the peer
// ends the session, and the end of the control stream ends the session.
func (l *Link) watchControl() {
	for {
		var reply Reply
		if err := ReadFrame(l.control, &reply); err != nil {
			_ = l.Session.Close()
			return
		}
		if reply.Status != 0 && !reply.OK() {
			l.mu.Lock()
			if l.err == nil {
				l.err = &RefusedError{Reply: reply}
			}
			l.mu.Unlock()
			// The dialer closes on a goaway, so the sender learns the frame arrived.
			_ = l.Session.Close()
			return
		}
	}
}

// HelloReply is the accepted Reply of the session handshake (ServerVersion and any params).
func (l *Link) HelloReply() Reply { return l.reply }

// Err returns the reason the peer gave for ending the session (a *RefusedError, e.g. status 401 when the login
// session was revoked), or ErrClosed once the session ended without one, or nil while it is open.
func (l *Link) Err() error {
	l.mu.Lock()
	err := l.err
	l.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-l.Done():
		return ErrClosed
	default:
		return nil
	}
}

// goawayLinger bounds how long a goaway waits for the peer to close the session.
const goawayLinger = 5 * time.Second

// Goaway writes reply on the control stream as the reason for ending the session, then closes it.
func (l *Link) Goaway(reply Reply) {
	l.mu.Lock()
	if l.err == nil {
		l.err = &RefusedError{Reply: reply}
	}
	l.mu.Unlock()
	_ = l.control.SetWriteDeadline(time.Now().Add(time.Second))
	_ = WriteFrame(l.control, reply)
	// Close the control stream before the session so the frame is flushed ahead of the teardown.
	_ = CloseWrite(l.control)
	go func() {
		// The peer closes once it read the goaway; a peer that does not is cut off after goawayLinger.
		timer := time.NewTimer(goawayLinger)
		defer timer.Stop()
		select {
		case <-l.Done():
		case <-timer.C:
		}
		_ = l.Session.Close()
	}()
}

// OpenDeviceStream opens a stream and sends header; it returns the stream positioned after the accepting Reply,
// or a *RefusedError carrying the refusal.
func (l *Link) OpenDeviceStream(ctx context.Context, header StreamHeader) (net.Conn, Reply, error) {
	conn, err := l.OpenStream(ctx)
	if err != nil {
		if goaway := l.Err(); goaway != nil && !errors.Is(goaway, ErrClosed) {
			return nil, Reply{}, goaway
		}
		return nil, Reply{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if err := WriteFrame(conn, header); err != nil {
		_ = conn.Close()
		return nil, Reply{}, contextError(ctx, err)
	}
	reply, err := ReadReplyWithin(conn, StreamReplyTimeout)
	if err != nil {
		_ = conn.Close()
		return nil, Reply{}, contextError(ctx, err)
	}
	if !stop() {
		_ = conn.Close()
		return nil, Reply{}, ctx.Err()
	}
	if !reply.OK() {
		_ = conn.Close()
		return nil, reply, &RefusedError{Reply: reply}
	}
	return conn, reply, nil
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// IncomingStream is an accepted stream whose header was read and which still awaits Accept or Refuse.
type IncomingStream struct {
	Header StreamHeader
	conn   net.Conn
	once   sync.Once
}

// Conn returns the stream before it was answered, for a caller that forwards it elsewhere.
func (s *IncomingStream) Conn() net.Conn { return s.conn }

// Accept answers 200 with params and returns the stream for the raw byte phase.
func (s *IncomingStream) Accept(params map[string]string) (net.Conn, error) {
	var err error = errors.New("relaymux: stream already answered")
	s.once.Do(func() {
		_ = s.conn.SetWriteDeadline(time.Now().Add(HandshakeTimeout))
		err = WriteFrame(s.conn, Reply{Status: http.StatusOK, Params: params})
		_ = s.conn.SetWriteDeadline(time.Time{})
	})
	if err != nil {
		_ = s.conn.Close()
		return nil, err
	}
	return s.conn, nil
}

// Refuse answers with a refusal and closes the stream.
func (s *IncomingStream) Refuse(reply Reply) {
	s.once.Do(func() {
		if reply.Status == 0 || reply.OK() {
			reply.Status = http.StatusInternalServerError
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(HandshakeTimeout))
		_ = WriteFrame(s.conn, reply)
		_ = CloseWrite(s.conn)
	})
	go func() {
		// Let the peer read the reply before the full close resets the stream: wait for its close, at most a second.
		buffer := make([]byte, 512)
		_ = s.conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, err := s.conn.Read(buffer); err != nil {
				break
			}
		}
		_ = s.conn.Close()
	}()
}

// AcceptHeader accepts the next stream and reads its StreamHeader. A stream whose header does not arrive in
// time or is malformed is dropped and the next one awaited.
func (l *Link) AcceptHeader(ctx context.Context) (*IncomingStream, error) {
	for {
		conn, err := l.AcceptStream(ctx)
		if err != nil {
			return nil, err
		}
		header, err := ReadHeader(conn)
		if err != nil {
			_ = conn.Close()
			continue
		}
		return &IncomingStream{Header: header, conn: conn}, nil
	}
}

// ServeStreams accepts streams until the link or ctx ends and runs handler for each in its own goroutine. It
// is the agent runtime's entry: handler maps Header.Purpose to today's bridge purpose handlers, calling
// Accept (with ParamStream=1 when it unwraps an rstream) or Refuse.
func (l *Link) ServeStreams(ctx context.Context, handler func(context.Context, *IncomingStream)) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		incoming, err := l.AcceptHeader(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if goaway := l.Err(); goaway != nil {
				return goaway
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler(ctx, incoming)
		}()
	}
}

// Handshake authenticates session as the dialer: it opens the control stream, sends hello and waits for the
// server's Reply. A refusal closes the session and returns a *RefusedError.
func Handshake(ctx context.Context, session Session, hello Hello) (*Link, error) {
	if hello.Version == 0 {
		hello.Version = Version
	}
	control, err := session.OpenStream(ctx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	if err := WriteFrame(control, hello); err != nil {
		_ = session.Close()
		return nil, contextError(ctx, err)
	}
	reply, err := ReadReply(control)
	if err != nil {
		_ = session.Close()
		return nil, contextError(ctx, fmt.Errorf("relaymux: handshake: %w", err))
	}
	if !stop() {
		return nil, ctx.Err()
	}
	if !reply.OK() {
		_ = session.Close()
		return nil, &RefusedError{Reply: reply}
	}
	return newLink(session, control, reply), nil
}

// PendingLink is a server-side session whose Hello was read and which awaits Accept or Refuse.
type PendingLink struct {
	Hello   Hello
	session Session
	control net.Conn
}

// AcceptHandshake waits for the dialer's control stream and reads its Hello, within HandshakeTimeout.
func AcceptHandshake(ctx context.Context, session Session) (*PendingLink, error) {
	ctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	control, err := session.AcceptStream(ctx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	var hello Hello
	if err := readFrameWithin(control, HandshakeTimeout, &hello); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("relaymux: read hello: %w", err)
	}
	return &PendingLink{Hello: hello, session: session, control: control}, nil
}

// Accept answers the Hello with a 200 Reply and returns the established link.
func (p *PendingLink) Accept(reply Reply) (*Link, error) {
	reply.Status = http.StatusOK
	_ = p.control.SetWriteDeadline(time.Now().Add(HandshakeTimeout))
	err := WriteFrame(p.control, reply)
	_ = p.control.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = p.session.Close()
		return nil, err
	}
	return newLink(p.session, p.control, reply), nil
}

// Refuse answers the Hello with a refusal and closes the session once the reply had a chance to arrive.
func (p *PendingLink) Refuse(reply Reply) {
	if reply.Status == 0 || reply.OK() {
		reply.Status = http.StatusForbidden
	}
	link := &Link{Session: p.session, control: p.control}
	link.Goaway(reply)
}
