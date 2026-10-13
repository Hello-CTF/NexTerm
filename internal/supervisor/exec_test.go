//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func awaitSessionCount(t *testing.T, supervisor *Supervisor, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		supervisor.mu.Lock()
		count := len(supervisor.sessions)
		supervisor.mu.Unlock()
		if count == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session count = %d, want %d", count, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func execSessionID(t *testing.T, supervisor *Supervisor) string {
	t.Helper()
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	for id, session := range supervisor.sessions {
		if session.exec != nil {
			return id
		}
	}
	t.Fatal("no exec session registered")
	return ""
}

func TestExecSplitStreams(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	stdout, stderr, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", `printf 'out-1'; printf 'err-1' >&2; printf 'out-2'; printf 'err-2' >&2`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if string(stdout) != "out-1out-2" {
		t.Fatalf("stdout = %q, want %q", stdout, "out-1out-2")
	}
	if string(stderr) != "err-1err-2" {
		t.Fatalf("stderr = %q, want %q", stderr, "err-1err-2")
	}
	awaitSessionCount(t, supervisor, 0)
}

func TestExecEnvAndDir(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exec-dir-marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", `ls; printf %s "$EXEC_MARKER"`},
		Dir:     dir,
		Env:     []string{"EXEC_MARKER=env-ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	output := string(stdout)
	if !strings.Contains(output, "exec-dir-marker") {
		t.Fatalf("stdout = %q, want the working directory listing", output)
	}
	if !strings.Contains(output, "env-ok") {
		t.Fatalf("stdout = %q, want the EXEC_MARKER value", output)
	}
}

func TestExecNonZeroExit(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	_, _, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", "exit 42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 42 {
		t.Fatalf("exit code = %d, want 42", exitCode)
	}
}

func TestExecSignalExit(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	_, _, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", "kill -TERM $$"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != -1 {
		t.Fatalf("exit code = %d, want -1 for a signal termination", exitCode)
	}
}

func TestExecSpawnFailure(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	_, _, _, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/nonexistent/nexterm-exec-missing"},
	})
	if err == nil {
		t.Fatal("exec of a missing executable succeeded")
	}
	awaitSessionCount(t, supervisor, 0)
}

func TestExecOutputCap(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	original := maxExecOutput
	maxExecOutput = 64 * 1024
	defer func() { maxExecOutput = original }()
	stdout, _, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"dd", "if=/dev/zero", "bs=4096", "count=64"},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("exec error = %v, want ErrUnavailable", err)
	}
	if exitCode != -1 {
		t.Fatalf("exit code = %d, want -1 on error", exitCode)
	}
	if len(stdout) > 64*1024+outputChunkSize {
		t.Fatalf("collected %d bytes beyond the cap plus one chunk", len(stdout))
	}
	awaitSessionCount(t, supervisor, 0)
}

func TestExecContextCancel(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, _, _, _, err := client.Exec(ctx, ExecSpec{
			Command: []string{"sleep", "30"},
		})
		done <- outcome{err}
	}()
	awaitSessionCount(t, supervisor, 1)
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("exec error = %v, want context.Canceled", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after cancellation")
	}
	awaitSessionCount(t, supervisor, 0)
}

func TestExecSessionNotListedNotAttachable(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, _, _, err := client.Exec(ctx, ExecSpec{
			Command: []string{"sleep", "30"},
		})
		done <- err
	}()
	awaitSessionCount(t, supervisor, 1)
	id := execSessionID(t, supervisor)
	infos, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("List = %+v, want the exec session hidden", infos)
	}
	if _, err := client.Attach(context.Background(), id, nil); err == nil {
		t.Fatal("attach to an exec session succeeded")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after cancellation")
	}
}

func TestExecThroughRemoteClient(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", server.SocketPath())
	}
	client := NewRemoteClient(dial, digest)
	stdout, stderr, exitCode, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", `printf 'remote-out'; printf 'remote-err' >&2; exit 3`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 3 {
		t.Fatalf("exit code = %d, want 3", exitCode)
	}
	if string(stdout) != "remote-out" || string(stderr) != "remote-err" {
		t.Fatalf("streams = %q / %q", stdout, stderr)
	}
}

func TestExecValidation(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	if _, _, _, _, err := client.Exec(context.Background(), ExecSpec{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty command error = %v, want ErrInvalidInput", err)
	}
	noPTY := false
	if _, err := client.Create(context.Background(), CreateOptions{Pty: &noPTY}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create with Pty=false error = %v, want ErrInvalidInput", err)
	}
}

// fakeExecUnawareServer acks the hello without a feature list, like a daemon
// built before exec support, and reports any frame the client sends next.
func fakeExecUnawareServer(t *testing.T, stateDir string, received chan<- frameType) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-old-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		kind, _, err := readFrame(conn)
		if err != nil || kind != frameHello {
			return
		}
		if err := writeFrame(conn, frameHelloAck, []byte(`{"version":2}`)); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		kind, _, err = readFrame(conn)
		if err != nil {
			received <- 0
			return
		}
		received <- kind
	}()
	return path
}

func TestExecUnsupportedWithoutFeature(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	received := make(chan frameType, 1)
	path := fakeExecUnawareServer(t, stateDir, received)
	client := NewClient(path, stateDir)
	_, _, _, _, err := client.Exec(context.Background(), ExecSpec{
		Command: []string{"/bin/sh", "-c", "true"},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("exec error = %v, want ErrUnsupported", err)
	}
	select {
	case kind := <-received:
		if kind != 0 {
			t.Fatalf("client sent frame %d to a server that never advertised exec", kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake server saw neither a frame nor idle timeout")
	}
}

func TestServerRejectsExecWithoutFeature(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn, err := net.DialTimeout("unix", server.SocketPath(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	kind, ack, err := readFrame(conn)
	if err != nil || kind != frameHelloAck {
		t.Fatalf("hello ack = %d, %v", kind, err)
	}
	var ackMsg helloMsg
	if err := unmarshalFrame(ack, &ackMsg); err != nil {
		t.Fatal(err)
	}
	if len(ackMsg.Features) == 0 {
		t.Fatal("server ack carries no feature list")
	}
	noPTY := false
	create, err := marshalFrame(frameCreate, createMsg{
		ID:      ids.New(),
		Command: []string{"/bin/sh", "-c", "true"},
		Pty:     &noPTY,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameCreate, create); err != nil {
		t.Fatal(err)
	}
	kind, response := readErrorFrame(t, conn)
	if kind != frameError {
		t.Fatalf("response frame = %d, want error", kind)
	}
	var msg errorMsg
	if err := unmarshalFrame(response, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Code != codeProtocol {
		t.Fatalf("error code = %q, want %q", msg.Code, codeProtocol)
	}
	awaitSessionCount(t, supervisor, 0)
}

func TestServerReapsExecWhenCreatedReplyFails(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn, err := net.DialTimeout("unix", server.SocketPath(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	hello, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest, Features: []string{featureExec}})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, hello); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameHelloAck {
		t.Fatalf("hello ack = %d, %v", kind, err)
	}
	noPTY := false
	create, err := marshalFrame(frameCreate, createMsg{ID: ids.New(), Command: []string{"/bin/sh", "-c", "sleep 30"}, Pty: &noPTY})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameCreate, create); err != nil {
		t.Fatal(err)
	}
	// Closing before reading the reply makes the server's frameCreated write fail;
	// the exec session must still be drained and reaped instead of leaking.
	_ = conn.Close()
	awaitSessionCount(t, supervisor, 0)
}

func TestExecEnvUsesLoginShellPATH(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	t.Setenv("PATH", "/nonexistent")
	stdout, _, exitCode, _, err := client.Exec(context.Background(), ExecSpec{Command: []string{"/bin/sh", "-c", `printf %s "$PATH"`}})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if path := string(stdout); path == "" || path == "/nonexistent" {
		t.Fatalf("exec PATH = %q, want the login-shell PATH overlay", path)
	}
}
