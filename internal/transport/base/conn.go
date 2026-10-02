package base

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
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
	appWriteMu     sync.Mutex
	writeMu        sync.Mutex
	writeCond      *sync.Cond
	pendingWrites  int64
	writeClosed    bool
	terminalErr    error
	closeWriteDone chan struct{}
	closeWriteErr  error
	closeOnce      sync.Once
	closeErr       error
	readOnce       sync.Once
	outputMode     atomic.Uint32
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
	}()
	return wrapped
}

func (c *StreamConn) Read(p []byte) (int, error) {
	if err := c.selectOutputMode(outputModeRaw); err != nil {
		return 0, err
	}
	c.readOnce.Do(func() {
		go func() {
			_, _ = io.Copy(c.peer, c.stream)
			_ = c.peer.Close()
		}()
	})
	n, err := c.conn.Read(p)
	if err != nil {
		if terminal := c.terminalError(); terminal != nil {
			return n, terminal
		}
	}
	return n, err
}

func (c *StreamConn) NextOutput(ctx context.Context) (OutputEvent, error) {
	ordered, ok := c.stream.(OrderedOutput)
	if !ok {
		return OutputEvent{}, ErrUnsupported
	}
	if err := c.selectOutputMode(outputModeOrdered); err != nil {
		return OutputEvent{}, err
	}
	return ordered.NextOutput(ctx)
}

func (c *StreamConn) selectOutputMode(mode outputMode) error {
	for {
		current := outputMode(c.outputMode.Load())
		if current == mode {
			return nil
		}
		if current != outputModeUnset {
			return ErrOutputMode
		}
		if c.outputMode.CompareAndSwap(uint32(outputModeUnset), uint32(mode)) {
			return nil
		}
	}
}

func (c *StreamConn) Write(p []byte) (int, error) {
	c.appWriteMu.Lock()
	defer c.appWriteMu.Unlock()
	c.writeMu.Lock()
	if c.terminalErr != nil {
		err := c.terminalErr
		c.writeMu.Unlock()
		return 0, err
	}
	if c.writeClosed {
		c.writeMu.Unlock()
		return 0, net.ErrClosed
	}
	if len(p) == 0 {
		c.writeMu.Unlock()
		return 0, nil
	}
	c.pendingWrites = int64(len(p))
	c.writeMu.Unlock()

	n, pipeErr := c.conn.Write(p)
	if n < len(p) || pipeErr != nil {
		resolution := pipeErr
		if resolution == nil {
			resolution = io.ErrShortWrite
		}
		c.resolveWrite(int64(len(p)-n), resolution)
	}
	c.writeMu.Lock()
	for c.pendingWrites > 0 && c.terminalErr == nil {
		c.writeCond.Wait()
	}
	terminal := c.terminalErr
	c.writeMu.Unlock()
	if terminal != nil {
		return n, terminal
	}
	return n, pipeErr
}

func (c *StreamConn) Close() error {
	c.closeOnce.Do(func() {
		c.resolveWrite(0, net.ErrClosed)
		c.closeErr = c.stream.Close()
		if errors.Is(c.closeErr, net.ErrClosed) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

func (c *StreamConn) CloseWrite() error {
	c.appWriteMu.Lock()
	defer c.appWriteMu.Unlock()
	c.writeMu.Lock()
	if c.writeClosed {
		done := c.closeWriteDone
		c.writeMu.Unlock()
		<-done
		return c.closeWriteErr
	}
	c.writeClosed = true
	for c.pendingWrites > 0 && c.terminalErr == nil {
		c.writeCond.Wait()
	}
	terminal := c.terminalErr
	c.writeMu.Unlock()
	if terminal == nil {
		terminal = c.stream.CloseWrite()
	}
	c.writeMu.Lock()
	c.closeWriteErr = terminal
	close(c.closeWriteDone)
	c.writeMu.Unlock()
	return terminal
}

func (c *StreamConn) resolveWrite(amount int64, err error) {
	c.writeMu.Lock()
	c.pendingWrites -= amount
	if c.pendingWrites < 0 {
		c.pendingWrites = 0
	}
	if err != nil && c.terminalErr == nil {
		c.terminalErr = err
	}
	if c.pendingWrites == 0 || c.terminalErr != nil {
		c.writeCond.Broadcast()
	}
	terminal := c.terminalErr != nil
	c.writeMu.Unlock()
	if terminal && err != nil {
		_ = c.conn.Close()
		_ = c.peer.Close()
	}
}

func (c *StreamConn) terminalError() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.terminalErr
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
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.conn.resolveWrite(int64(len(p)), err)
	return n, err
}
