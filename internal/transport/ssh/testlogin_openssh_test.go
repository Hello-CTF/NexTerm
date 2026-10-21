package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// openSSHTestServer is a real OpenSSH sshd started unprivileged on
// 127.0.0.1 with test-generated host and user keys. It skips the test when
// the environment cannot provide a usable sshd, so the suite stays
// dependency-free; where sshd exists, the test-login handshake runs against
// the real OpenSSH implementation.
type openSSHTestServer struct {
	cmd         *exec.Cmd
	stderr      *bytes.Buffer
	port        int
	user        string
	hostKey     gossh.PublicKey
	userKeyPEM  []byte
	otherKeyPEM []byte
}

func startOpenSSHTestServer(t *testing.T, port int, userKeyPEM []byte) *openSSHTestServer {
	t.Helper()
	binary := openSSHTestBinary(t)
	current, err := user.Current()
	if err != nil {
		t.Skipf("current user is unavailable: %v", err)
	}
	dir := t.TempDir()
	hostSigner, hostPrivate := openSSHTestKeygen(t)
	userSigner, userPrivate := openSSHTestKeygen(t)
	_, otherPrivate := openSSHTestKeygen(t)
	authorizedKey := userSigner.PublicKey()
	if userKeyPEM != nil {
		signer, err := gossh.ParsePrivateKey(userKeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		authorizedKey = signer.PublicKey()
	}
	hostKeyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	if err := os.WriteFile(hostKeyPath, openSSHTestKeyPEM(t, hostPrivate), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizedKeysPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorizedKeysPath, gossh.MarshalAuthorizedKey(authorizedKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if port == 0 {
		port = openSSHTestFreePort(t)
	}
	configPath := filepath.Join(dir, "sshd_config")
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\n"+
		"AuthorizedKeysFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n",
		port, hostKeyPath, filepath.Join(dir, "sshd.pid"), authorizedKeysPath)
	if runtime.GOOS != "windows" {
		// Windows OpenSSH rejects UsePAM as an unsupported option.
		config += "UsePAM no\n"
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	// The validation must stay bounded: Windows OpenSSH sshd -t never exits
	// after printing a config error while its output is piped, which would
	// otherwise hang the whole suite on CombinedOutput.
	checkCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(checkCtx, binary, "-t", "-f", configPath).CombinedOutput(); err != nil {
		t.Skipf("sshd cannot use the test configuration: %v: %s", err, output)
	}
	stderr := &bytes.Buffer{}
	cmd := exec.Command(binary, "-D", "-e", "-f", configPath)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("sshd failed to start: %v", err)
	}
	server := &openSSHTestServer{
		cmd: cmd, stderr: stderr, port: port, user: current.Username,
		hostKey: hostSigner.PublicKey(), userKeyPEM: openSSHTestKeyPEM(t, userPrivate),
		otherKeyPEM: openSSHTestKeyPEM(t, otherPrivate),
	}
	t.Cleanup(server.stop)
	openSSHTestWaitReady(t, server)
	return server
}

func (s *openSSHTestServer) stop() {
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	s.cmd.Wait()
}

func openSSHTestBinary(t *testing.T) string {
	t.Helper()
	if binary, err := exec.LookPath("sshd"); err == nil {
		return binary
	}
	for _, candidate := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	t.Skip("OpenSSH sshd is not available; skipping real-sshd test-login integration test")
	return ""
}

func openSSHTestKeygen(t *testing.T) (gossh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, private
}

func openSSHTestKeyPEM(t *testing.T, private ed25519.PrivateKey) []byte {
	t.Helper()
	block, err := gossh.MarshalPrivateKey(private, "nexterm-test")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func openSSHTestFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func openSSHTestWaitReady(t *testing.T, server *openSSHTestServer) {
	t.Helper()
	address := endpointString("127.0.0.1", server.port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			banner := make([]byte, 64)
			n, _ := conn.Read(banner)
			conn.Close()
			if n > 0 && strings.HasPrefix(string(banner[:n]), "SSH-2.0-") {
				return
			}
		}
		if server.cmd.ProcessState != nil {
			t.Skipf("sshd exited before becoming ready: %s", server.stderr.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd did not become ready at %s: %s", address, server.stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestTestLoginOpenSSHHostKeyTrustAndAuth(t *testing.T) {
	fixture := startOpenSSHTestServer(t, 0, nil)
	store := NewMemoryHostKeyStore()
	cfg := Config{
		Host: "127.0.0.1", Port: fixture.port, User: fixture.user,
		Auth: AuthConfig{Method: AuthKey, KeyPEM: fixture.userKeyPEM}, HostKeys: store,
		ConnectTimeout: 5 * time.Second,
	}

	err := TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindHostKeyPending)
	if !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("real sshd unknown host key lost the pending sentinel: %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || !keyErr.Pending || keyErr.Presented.Fingerprint != gossh.FingerprintSHA256(fixture.hostKey) {
		t.Fatalf("real sshd pending details = %#v", err)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureHostKey {
		t.Fatalf("real sshd pending class = %q", got)
	}
	if keys, _ := store.HostKeys(context.Background(), cfg.Host, cfg.Port); len(keys) != 0 {
		t.Fatalf("real sshd probe persisted an unknown key: %+v", keys)
	}

	if err := store.PutHostKey(context.Background(), newHostKey(cfg.Host, cfg.Port, fixture.hostKey), false); err != nil {
		t.Fatal(err)
	}
	if err := TestLogin(context.Background(), cfg); err != nil {
		t.Fatalf("real sshd test login with a trusted key and authorized credential: %v", err)
	}

	cfg.Auth = AuthConfig{Method: AuthKey, KeyPEM: fixture.otherKeyPEM}
	err = TestLogin(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Op != "authentication" {
		t.Fatalf("real sshd auth failure op = %q", connectErr.Op)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureAuth {
		t.Fatalf("real sshd auth failure class = %q", got)
	}

	cfg.Auth = AuthConfig{Method: AuthKey, KeyPEM: fixture.userKeyPEM}
	fixture.stop()
	rotated := startOpenSSHTestServer(t, cfg.Port, fixture.userKeyPEM)
	err = TestLogin(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindHostKeyChanged)
	if !errors.As(err, &keyErr) || keyErr.Pending || keyErr.Presented.Fingerprint != gossh.FingerprintSHA256(rotated.hostKey) {
		t.Fatalf("real sshd changed details = %#v", err)
	}
	if len(keyErr.Known) != 1 || keyErr.Known[0].Fingerprint != gossh.FingerprintSHA256(fixture.hostKey) {
		t.Fatalf("real sshd changed known keys = %+v", keyErr.Known)
	}
	if got := ClassifyLoginFailure(err); got != LoginFailureHostKey {
		t.Fatalf("real sshd changed class = %q", got)
	}
	keys, err := store.HostKeys(context.Background(), cfg.Host, cfg.Port)
	if err != nil || len(keys) != 1 || keys[0].Fingerprint != gossh.FingerprintSHA256(fixture.hostKey) {
		t.Fatalf("real sshd changed probe overwrote the trust store: %+v, %v", keys, err)
	}
}
