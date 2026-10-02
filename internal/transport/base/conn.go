package base

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type WriteCloseStream interface {
	io.ReadWriteCloser
	CloseWrite() error
}

type StreamConn struct {
	stream         WriteCloseStream
	conn           net.Conn
	peer           net.Conn
	local          net.Addr
	remote         net.Addr
	writeMu        sync.Mutex
	writeCond      *sync.Cond
	pendingWrites  int64
	writeClosed    bool
	closeWriteDone chan struct{}
	closeWriteErr  error
	closeOnce      sync.Once
	closeErr       error
}

func NewStreamConn(stream WriteCloseStream, local, remote net.Addr) *StreamConn {
	conn, peer := net.Pipe()
	wrapped := &StreamConn{
		stream:         stream,
		conn:           conn,
		peer:           peer,
		local:          local,
		remote:         remote,
		closeWriteDone: make(chan struct{}),
	}
	wrapped.writeCond = sync.NewCond(&wrapped.writeMu)
	go func() {
		_, _ = io.Copy(&flushWriter{conn: wrapped}, peer)
		_ = stream.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(peer, stream)
		_ = peer.Close()
	}()
	return wrapped
}

func (c *StreamConn) Read(p []byte) (int, error) {
	return c.conn.Read(p)
}

func (c *StreamConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	if c.writeClosed {
		c.writeMu.Unlock()
		return 0, net.ErrClosed
	}
	c.pendingWrites += int64(len(p))
	c.writeMu.Unlock()
	n, err := c.conn.Write(p)
	if n < len(p) {
		c.written(int64(len(p) - n))
	}
	return n, err
}

func (c *StreamConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
		_ = c.peer.Close()
		c.closeErr = c.stream.Close()
		if errors.Is(c.closeErr, net.ErrClosed) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

func (c *StreamConn) CloseWrite() error {
	c.writeMu.Lock()
	if c.writeClosed {
		done := c.closeWriteDone
		c.writeMu.Unlock()
		<-done
		return c.closeWriteErr
	}
	c.writeClosed = true
	for c.pendingWrites > 0 {
		c.writeCond.Wait()
	}
	c.writeMu.Unlock()
	err := c.stream.CloseWrite()
	c.writeMu.Lock()
	c.closeWriteErr = err
	close(c.closeWriteDone)
	c.writeMu.Unlock()
	return err
}

func (c *StreamConn) written(amount int64) {
	c.writeMu.Lock()
	c.pendingWrites -= amount
	if c.pendingWrites == 0 {
		c.writeCond.Broadcast()
	}
	c.writeMu.Unlock()
}

func (c *StreamConn) LocalAddr() net.Addr {
	return c.local
}

func (c *StreamConn) RemoteAddr() net.Addr {
	return c.remote
}

func (c *StreamConn) SetDeadline(deadline time.Time) error {
	return c.conn.SetDeadline(deadline)
}

func (c *StreamConn) SetReadDeadline(deadline time.Time) error {
	return c.conn.SetReadDeadline(deadline)
}

func (c *StreamConn) SetWriteDeadline(deadline time.Time) error {
	return c.conn.SetWriteDeadline(deadline)
}

type flushWriter struct {
	conn *StreamConn
}

func (w *flushWriter) Write(p []byte) (int, error) {
	n, err := w.conn.stream.Write(p)
	w.conn.written(int64(len(p)))
	return n, err
}
