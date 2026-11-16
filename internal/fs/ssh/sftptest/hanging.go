// Package sftptest serves a real SFTP server through a proxy that can stop answering selected requests, the way a
// server blocked on a hung NFS or FUSE mount never answers them. It is for tests only.
package sftptest

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// Request names one kind of SFTP request: a packet type (draft-ietf-secsh-filexfer-02), and for extended requests
// the extension name.
type Request struct {
	Type     byte
	Extended string
}

var (
	Open        = Request{Type: 3}
	Read        = Request{Type: 5}
	Write       = Request{Type: 6}
	Lstat       = Request{Type: 7}
	Setstat     = Request{Type: 9}
	Mkdir       = Request{Type: 14}
	RealPath    = Request{Type: 16}
	Stat        = Request{Type: 17}
	Rename      = Request{Type: 18}
	ReadLink    = Request{Type: 19}
	PosixRename = Request{Type: 200, Extended: "posix-rename@openssh.com"}
	Hardlink    = Request{Type: 200, Extended: "hardlink@openssh.com"}
)

// HangingServer forwards requests to the real server except the ones Hang selected, which it swallows unanswered
// until Release forwards them late.
type HangingServer struct {
	mu      sync.Mutex
	hang    map[Request]bool
	held    chan Request
	pending [][]byte
	// writeMu serializes packets to the server between the forwarding loop and Release.
	writeMu sync.Mutex
	server  io.Writer
}

// Hang selects the requests to swallow from now on, replacing the previous selection; no arguments forward everything.
// Requests already swallowed stay unanswered until Release.
func (h *HangingServer) Hang(requests ...Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hang = make(map[Request]bool, len(requests))
	for _, request := range requests {
		h.hang[request] = true
	}
	// Requests held by an earlier phase must not satisfy the next phase's wait.
	for {
		select {
		case <-h.held:
		default:
			return
		}
	}
}

// Held reports each swallowed request (dropping reports nobody reads), so a test knows its caller is waiting on an
// unanswered request.
func (h *HangingServer) Held() <-chan Request {
	return h.held
}

// Release stops swallowing requests and forwards every request swallowed so far, the way a hung mount that recovers
// answers the requests that waited on it.
func (h *HangingServer) Release() {
	h.mu.Lock()
	h.hang = nil
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	for _, packet := range pending {
		h.send(packet)
	}
}

// swallow keeps packet for Release when request is selected.
func (h *HangingServer) swallow(request Request, packet []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hang[request] {
		return false
	}
	h.pending = append(h.pending, packet)
	return true
}

func (h *HangingServer) send(packet []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	_, err := h.server.Write(packet)
	return err
}

func (h *HangingServer) forward(from io.Reader) {
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(from, header); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(header))
		if _, err := io.ReadFull(from, body); err != nil {
			return
		}
		packet := append(append([]byte(nil), header...), body...)
		if request, ok := parseRequest(body); ok && h.swallow(request, packet) {
			select {
			case h.held <- request:
			default:
			}
			continue
		}
		if err := h.send(packet); err != nil {
			return
		}
	}
}

// parseRequest reads the type and, for an extended request, the extension name: type, uint32 id, string name.
func parseRequest(body []byte) (Request, bool) {
	if len(body) == 0 {
		return Request{}, false
	}
	request := Request{Type: body[0]}
	if request.Type == 200 && len(body) >= 9 {
		size := int(binary.BigEndian.Uint32(body[5:9]))
		if 9+size <= len(body) {
			request.Extended = string(body[9 : 9+size])
		}
	}
	return request, true
}

// NewHangingClient serves a fresh temporary directory with a real SFTP server behind a HangingServer and returns a
// client connected through it, the directory, and the proxy. Everything closes when the test ends; closing the client
// fails the requests still waiting on swallowed ones.
func NewHangingClient(t testing.TB, options ...sftp.ClientOption) (*sftp.Client, string, *HangingServer) {
	t.Helper()
	root := t.TempDir()
	clientSide, proxyClient := net.Pipe()
	proxyServer, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	hanging := &HangingServer{held: make(chan Request, 16), server: proxyServer}
	go hanging.forward(proxyClient)
	go func() { _, _ = io.Copy(proxyClient, proxyServer) }()
	client, err := sftp.NewClientPipe(clientSide, clientSide, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
		for _, conn := range []net.Conn{clientSide, proxyClient, proxyServer, serverSide} {
			conn.Close()
		}
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("SFTP test server did not stop")
		}
	})
	return client, root, hanging
}
