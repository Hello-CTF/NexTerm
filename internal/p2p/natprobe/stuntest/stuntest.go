// Package stuntest is an in-process STUN Binding responder for tests.
package stuntest

import (
	"net"
	"sync"

	"github.com/pion/stun/v4"
)

// Server answers STUN Binding requests on one packet conn with the source
// address it saw, optionally rewritten.
type Server struct {
	pc      net.PacketConn
	rewrite func(*net.UDPAddr) *net.UDPAddr
	mu      sync.Mutex
	silent  bool
	count   int
	done    chan struct{}
}

// Serve starts answering on pc until Close. rewrite may be nil.
func Serve(pc net.PacketConn, rewrite func(*net.UDPAddr) *net.UDPAddr) *Server {
	s := &Server{pc: pc, rewrite: rewrite, done: make(chan struct{})}
	go s.loop()
	return s
}

// Addr is the address the server listens on.
func (s *Server) Addr() net.Addr { return s.pc.LocalAddr() }

// SetSilent makes the server read requests without answering.
func (s *Server) SetSilent(v bool) {
	s.mu.Lock()
	s.silent = v
	s.mu.Unlock()
}

// Requests returns how many Binding requests arrived.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Close stops the server and waits for its loop to exit.
func (s *Server) Close() error {
	err := s.pc.Close()
	<-s.done
	return err
}

func (s *Server) loop() {
	defer close(s.done)
	buf := make([]byte, 1500)
	for {
		n, from, err := s.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if m.Decode() != nil || m.Type != stun.BindingRequest {
			continue
		}
		s.mu.Lock()
		s.count++
		silent := s.silent
		s.mu.Unlock()
		if silent {
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			if ua, err = net.ResolveUDPAddr("udp", from.String()); err != nil {
				continue
			}
		}
		if s.rewrite != nil {
			ua = s.rewrite(ua)
		}
		resp, err := stun.Build(m, stun.BindingSuccess, &stun.XORMappedAddress{IP: ua.IP, Port: ua.Port}, stun.Fingerprint)
		if err != nil {
			continue
		}
		_, _ = s.pc.WriteTo(resp.Raw, from)
	}
}
