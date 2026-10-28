package supervisor

import (
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

type flakyListener struct {
	mu     sync.Mutex
	errs   []error
	calls  int
	closed chan struct{}
	once   sync.Once
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *flakyListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *flakyListener) Addr() net.Addr { return &net.UnixAddr{Name: "fake", Net: "unix"} }

func (l *flakyListener) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// Transient accept errors must not end the loop; a closed listener must.
func TestAcceptLoopSurvivesTransientErrors(t *testing.T) {
	previous := acceptBackoffMin
	acceptBackoffMin = time.Millisecond
	t.Cleanup(func() { acceptBackoffMin = previous })

	listener := &flakyListener{
		errs:   []error{syscall.EMFILE, syscall.ECONNABORTED, syscall.EMFILE},
		closed: make(chan struct{}),
	}
	server := &Server{listener: listener, conns: map[*serverConn]struct{}{}}
	server.wg.Add(1)
	go server.acceptLoop()

	deadline := time.Now().Add(5 * time.Second)
	for listener.callCount() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("accept loop stopped after %d calls, want it to keep retrying", listener.callCount())
		}
		time.Sleep(time.Millisecond)
	}

	server.mu.Lock()
	server.closed = true
	server.mu.Unlock()
	_ = listener.Close()
	done := make(chan struct{})
	go func() { server.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not exit after listener close")
	}
}
