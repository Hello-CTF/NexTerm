package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/remotedesktop"
)

type fakeStreamConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	in     chan []byte
	out    chan []byte
	once   sync.Once
}

func newFakeStreamConn(buffer int) *fakeStreamConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeStreamConn{ctx: ctx, cancel: cancel, in: make(chan []byte, 8), out: make(chan []byte, buffer)}
}

func (c *fakeStreamConn) Receive() ([]byte, error) {
	select {
	case frame := <-c.in:
		return frame, nil
	case <-c.ctx.Done():
		return nil, errors.New("closed")
	}
}

func (c *fakeStreamConn) Send(data []byte) error {
	select {
	case c.out <- data:
		return nil
	case <-c.ctx.Done():
		return errors.New("closed")
	}
}

func (c *fakeStreamConn) Close() error {
	c.once.Do(c.cancel)
	return nil
}
func (c *fakeStreamConn) Context() context.Context { return c.ctx }

type recordingService struct {
	ident  remotedesktop.Identity
	ticket string
	first  []byte
}

func (s *recordingService) Serve(_ context.Context, conn remotedesktop.FrameConn, ident remotedesktop.Identity, ticket string) error {
	s.ident, s.ticket = ident, ticket
	frame, err := conn.ReadFrame()
	if err != nil {
		return err
	}
	s.first = frame
	return conn.WriteFrame(append([]byte("ok:"), frame...))
}

func TestRemoteDesktopStreamAdaptsFramesWithoutIdentity(t *testing.T) {
	conn := newFakeStreamConn(1)
	conn.in <- []byte(remotedesktop.TicketPreamble + "t1")
	service := &recordingService{}
	serveRemoteDesktopStream(service, conn)
	if service.ident != (remotedesktop.Identity{}) || service.ticket != "" {
		t.Fatalf("identity/ticket = %+v %q; the ticket must come from the first frame", service.ident, service.ticket)
	}
	if got := string(<-conn.out); got != "ok:"+remotedesktop.TicketPreamble+"t1" {
		t.Fatalf("sent = %q", got)
	}
}

func TestRemoteDesktopStreamNameIsStable(t *testing.T) {
	if remotedesktop.StreamName != "remotedesktop" {
		t.Fatalf("stream name = %q", remotedesktop.StreamName)
	}
}
