package supervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

func runBridge(ctx context.Context, config HelperConfig) error {
	conn, err := DialHelperBridge(ctx, config.StateDir)
	if err != nil {
		return err
	}
	var stdin io.Reader = os.Stdin
	if env := SSHSessionEnv(os.Getenv); len(env) > 0 {
		stdin = &filteredReader{source: os.Stdin, filter: NewCreateEnvFilter(env)}
	}
	return bridgeIO(stdin, os.Stdout, conn)
}

// SSHSessionEnvKeys are the variables sshd gives a session to say where it came from. A helper outlives the
// connection that started it, so the bridge of each connection hands its own values to the sessions it creates;
// programs use them to tell a remote terminal from a local one (to copy through OSC 52, for example).
var SSHSessionEnvKeys = []string{"SSH_CONNECTION", "SSH_CLIENT"}

// SSHSessionEnv returns the SSHSessionEnvKeys entries that getenv reports.
func SSHSessionEnv(getenv func(string) string) []string {
	var env []string
	for _, key := range SSHSessionEnvKeys {
		if value := getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// SSHConnectionEnv states a connection that reaches this host the way sshd states the connection of a session:
// client is the peer and server this host's end. An address that is not TCP is reported as 0.0.0.0 port 0, so
// programs still see a remote session.
func SSHConnectionEnv(client, server net.Addr) []string {
	clientIP, clientPort := tcpEndpoint(client)
	serverIP, serverPort := tcpEndpoint(server)
	return []string{
		fmt.Sprintf("SSH_CONNECTION=%s %d %s %d", clientIP, clientPort, serverIP, serverPort),
		fmt.Sprintf("SSH_CLIENT=%s %d %d", clientIP, clientPort, serverPort),
	}
}

func tcpEndpoint(addr net.Addr) (string, int) {
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP != nil {
		return tcp.IP.String(), tcp.Port
	}
	return "0.0.0.0", 0
}

// filteredReader passes the bytes read from source through a StreamFilter.
type filteredReader struct {
	source  io.Reader
	filter  *StreamFilter
	pending []byte
	buffer  [32 * 1024]byte
}

func (r *filteredReader) Close() error {
	if closer, ok := r.source.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (r *filteredReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		n, err := r.source.Read(r.buffer[:])
		if n > 0 {
			r.pending, _ = r.filter.Filter(r.buffer[:n])
		}
		if err != nil && len(r.pending) == 0 {
			return 0, err
		}
		if err != nil {
			break
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// DialHelperBridge opens a byte-transparent connection to the existing helper.
// The caller ensures helper availability before opening a bridge. When only an
// older helper serves the state directory, the returned connection answers the
// client's hello with a helper-upgrade rejection, or replaces it when the
// hello carries the consent token of that rejection (see legacyBridgeConn).
func DialHelperBridge(ctx context.Context, stateDir string) (net.Conn, error) {
	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve supervisor helper endpoint: %w", err)
	}
	conn, err := dialSocket(ctx, endpoint)
	if err != nil {
		if absolute, absErr := filepath.Abs(stateDir); absErr == nil {
			if version, ok := legacyHelperVersion(ctx, absolute); ok {
				return legacyBridgeConn(absolute, version), nil
			}
		}
		return nil, fmt.Errorf("%w: dial supervisor helper: %v", ErrUnavailable, err)
	}
	return conn, nil
}

func bridgeIO(stdin io.Reader, stdout io.Writer, conn net.Conn) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdout, conn)
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	go func() {
		_, _ = io.Copy(conn, stdin)
		_ = conn.Close()
	}()
	wg.Wait()
	return nil
}
