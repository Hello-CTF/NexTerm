package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHHostKeyExecPTYAndSFTP(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.AutoAcceptUnknown = false
	if _, err := Connect(context.Background(), cfg); !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("first connection = %v", err)
	}
	presented := newHostKey(cfg.Host, cfg.Port, server.hostKey)
	cfg.HostKeyApproval = &HostKeyApproval{Fingerprint: presented.Fingerprint}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	t.Run("exec and exit semantics", func(t *testing.T) {
		result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
		if err != nil || result.Stdout != "stdout" || result.Stderr != "stderr" || result.ExitCode == nil || *result.ExitCode != 0 || result.Truncated {
			t.Fatalf("exec = %+v, %v", result, err)
		}
		result, err = client.Exec(context.Background(), "exit42", base.ExecOptions{})
		if err != nil || result.ExitCode == nil || *result.ExitCode != 42 || result.Stderr != "failed" {
			t.Fatalf("exit 42 = %+v, %v", result, err)
		}
		result, err = client.Exec(context.Background(), "large", base.ExecOptions{Limits: base.OutputLimits{Stdout: 8, Stderr: 4}})
		if err != nil || len(result.Stdout) != 8 || len(result.Stderr) != 4 || !result.Truncated {
			t.Fatalf("limited exec = %+v, %v", result, err)
		}
		if _, err := client.Exec(context.Background(), "basic", base.ExecOptions{ExpectedGeneration: client.Generation() + 1}); !errors.Is(err, base.ErrStaleGeneration) {
			t.Fatalf("stale generation = %v", err)
		}
		if _, err := client.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("streaming exec", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		channel, err := client.OpenExec(ctx, "stream", base.ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer channel.Close()
		if channel.ID() == "" || channel.Generation() != client.Generation() {
			t.Fatalf("channel identity = %q/%d", channel.ID(), channel.Generation())
		}
		if _, err := channel.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		if err := channel.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(channel)
		if err != nil || string(data) != "stream:abc" {
			t.Fatalf("stream = %q, %v", data, err)
		}
		if err := channel.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("PTY and shared connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		pty, err := client.OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24, ExpectedGeneration: client.Generation()})
		if err != nil {
			t.Fatal(err)
		}
		defer pty.Close()
		ready := make([]byte, 5)
		if _, err := io.ReadFull(pty, ready); err != nil || string(ready) != "ready" {
			t.Fatalf("PTY ready = %q, %v", ready, err)
		}
		if _, err := pty.Write([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, 8)
		if _, err := io.ReadFull(pty, echo); err != nil || string(echo) != "echo:abc" {
			t.Fatalf("PTY echo = %q, %v", echo, err)
		}
		if err := pty.Resize(ctx, 120, 40); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for (server.cols.Load() != 120 || server.rows.Load() != 40) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if server.cols.Load() != 120 || server.rows.Load() != 40 {
			t.Fatalf("PTY size = %dx%d", server.cols.Load(), server.rows.Load())
		}
		result, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
		if err != nil || result.Stdout != "stdout" || !client.IsAlive() {
			t.Fatalf("exec with open PTY = %+v, %v", result, err)
		}
	})

	t.Run("SFTP shares client", func(t *testing.T) {
		filesystem, err := client.SFTP(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := filesystem.WriteFile(context.Background(), "shared.txt", []byte("sftp"), false); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errorsFound := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := client.Exec(context.Background(), "basic", base.ExecOptions{})
				errorsFound <- err
			}()
		}
		wg.Wait()
		close(errorsFound)
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
		data, err := filesystem.ReadFile(context.Background(), "shared.txt", 100)
		if err != nil || string(data) != "sftp" {
			t.Fatalf("shared SFTP = %q, %v", data, err)
		}
	})
}

func TestSSHCancellationKeepsSharedConnection(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Exec(ctx, "sleep", base.ExecOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exec cancellation = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("exec cancellation did not unblock")
	}
	if !client.IsAlive() {
		t.Fatal("cancelling one exec closed the shared connection")
	}
	if _, err := client.Exec(context.Background(), "basic", base.ExecOptions{}); err != nil {
		t.Fatalf("exec after cancellation: %v", err)
	}

	started = time.Now()
	_, err := client.OpenExec(context.Background(), "block-request", base.ExecOptions{Timeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("startup cancellation did not unblock")
	}
	server.blockOnce.Do(func() { close(server.blockExec) })
}

func TestClientCloseUnblocksStalledSFTP(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.stallSFTP.Store(true)
	defer func() { server.blockOnce.Do(func() { close(server.blockExec) }) }()
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	sftpResult := make(chan error, 1)
	go func() {
		_, err := client.SFTP(context.Background())
		sftpResult <- err
	}()
	select {
	case <-server.sftpStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("SFTP initialization did not reach the stalled version handshake")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- client.Close() }()
	select {
	case <-closeResult:
	case <-time.After(2 * time.Second):
		t.Fatal("Client.Close hung behind stalled SFTP initialization")
	}
	select {
	case err := <-sftpResult:
		if err == nil {
			t.Fatal("stalled SFTP initialization unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the client did not unblock SFTP initialization")
	}
}

func TestSSHConnectProgressStagesDirect(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	var events []ConnectProgress
	cfg.Progress = func(progress ConnectProgress) {
		events = append(events, progress)
	}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	want := []ConnectProgress{
		{Stage: ConnectStageAuth, Hop: 0, Hops: 0, Host: cfg.Host, Port: cfg.Port},
		{Stage: ConnectStageDial, Hop: 0, Hops: 0, Host: cfg.Host, Port: cfg.Port},
		{Stage: ConnectStageHandshake, Hop: 0, Hops: 0, Host: cfg.Host, Port: cfg.Port},
	}
	if !slices.Equal(events, want) {
		t.Fatalf("progress events = %+v, want %+v", events, want)
	}
}

func TestSSHKeepalive(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.KeepAliveInterval = 20 * time.Millisecond
	cfg.KeepAliveMax = 3
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(time.Second)
	for server.keepalives.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if server.keepalives.Load() == 0 {
		t.Fatal("server received no SSH keepalive request")
	}
	if !client.IsAlive() {
		t.Fatal("answered keepalive closed the connection")
	}
}

func TestSSHPrivateKeyAuthentication(t *testing.T) {
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, signer.PublicKey())
	block, err := gossh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server, AuthConfig{Method: AuthKey, KeyPEM: pem.EncodeToMemory(block)})
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSSHAgentAuthentication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SSH agent integration uses SSH_AUTH_SOCK over a Unix socket; Windows runs the compile-only coverage")
	}
	_, private, signer := generateClientKey(t)
	server := newTestSSHServer(t, signer.PublicKey())
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				agent.ServeAgent(keyring, conn)
				conn.Close()
			}()
		}
	}()
	client := connectTestClient(t, server, AuthConfig{Method: AuthAgent, AgentSocket: socket})
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSSHDirectDialAndExecConn(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	echo := startEchoListener(t, "tcp", "127.0.0.1:0")
	conn, err := client.DialContext(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	assertConnEcho(t, conn, "tcp")
	conn.Close()
	unixRoot := t.TempDir()
	t.Run("unix direct-streamlocal", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("direct-streamlocal integration needs a local Unix echo socket; Windows has compile-only coverage")
		}
		socket := filepath.Join(unixRoot, "e.sock")
		unixEcho := startEchoListener(t, "unix", socket)
		defer unixEcho.Close()
		conn, err := client.DialContext(context.Background(), "unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		assertConnEcho(t, conn, "unix")
		conn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "dial-stdio", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	if _, err := execConn.Write([]byte("docker")); err != nil {
		t.Fatal(err)
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(execConn)
	waitErr := execConn.Wait(ctx)
	stderr, _ := io.ReadAll(execConn.Stderr())
	if err != nil || string(data) != "docker" || waitErr != nil {
		t.Fatalf("exec conn = %q, %v; wait = %v; stderr = %q", data, err, waitErr, stderr)
	}
}

func TestSSHExecConnOrderedOutput(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "ordered-live", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	expected := []struct {
		data   string
		stderr bool
	}{
		{data: "out-1"},
		{data: "err-1", stderr: true},
		{data: "out-2"},
		{data: "err-2", stderr: true},
	}
	for index, want := range expected {
		event, err := execConn.NextOutput(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(event.Data) != want.data || event.Stderr != want.stderr || event.Sequence != uint64(index+1) {
			t.Fatalf("event %d = %+v", index, event)
		}
		if _, err := execConn.Write([]byte{byte(index)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := execConn.NextOutput(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("ordered stream EOF = %v", err)
	}
	if err := execConn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := execConn.Read(make([]byte, 1)); !errors.Is(err, base.ErrOutputMode) {
		t.Fatalf("raw read after ordered selection = %v", err)
	}
}

func TestSSHExecConnCloseWriteAfterRemoteClose(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execConn, err := client.OpenExecConn(ctx, "basic", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer execConn.Close()
	if err := execConn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := execConn.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite after remote close = %v", err)
	}
}

func TestSSHWaitDrainsFullRawQueues(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.Exec(ctx, "full-queues", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != strings.Repeat("o", 33) || result.Stderr != strings.Repeat("e", 33) {
		t.Fatalf("full queue output = %q/%q", result.Stdout, result.Stderr)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("full queue exit = %+v", result.ExitCode)
	}
}

func TestSSHPOSIXRootsComeFromTargetSFTP(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	filesystem, err := client.FileSystem(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := filesystem.(base.RootsProvider)
	if !ok {
		t.Fatal("SSH filesystem does not provide roots")
	}
	roots, err := provider.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if roots.PathStyle != base.PathStylePOSIX || !roots.Supported || len(roots.Roots) != 1 || roots.Roots[0].Path != "/" || roots.HomePath == "" {
		t.Fatalf("POSIX roots = %+v", roots)
	}
}

type targetExecutor struct {
	mu       sync.Mutex
	commands []string
	result   base.ExecResult
	err      error
	handle   func(string) (base.ExecResult, error)
}

func (e *targetExecutor) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	e.mu.Lock()
	e.commands = append(e.commands, command)
	e.mu.Unlock()
	if e.handle != nil {
		return e.handle(command)
	}
	return e.result, e.err
}

func (e *targetExecutor) commandList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.commands...)
}

type targetRealPath struct {
	home  string
	err   error
	calls []string
}

func (r *targetRealPath) RealPath(path string) (string, error) {
	r.calls = append(r.calls, path)
	return r.home, r.err
}

func targetResult(output string, code int) base.ExecResult {
	return base.ExecResult{Stdout: output, ExitCode: &code}
}

func windowsRootsOutput(home string, roots ...[2]string) string {
	items := make([]string, 0, len(roots))
	for _, root := range roots {
		items = append(items, fmt.Sprintf(`{"n":%q,"r":%q}`, root[0], root[1]))
	}
	return fmt.Sprintf(`{"windows":true,"supported":true,"status":"ok","home":%q,"roots":[%s]}`, home, strings.Join(items, ","))
}

func decodePowerShellCommand(t *testing.T, command string) string {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) == 0 {
		t.Fatal("empty PowerShell command")
	}
	data, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%2 != 0 {
		t.Fatalf("odd UTF-16 payload length %d", len(data))
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(data[index*2:])
	}
	return string(utf16.Decode(units))
}

func TestSSHWindowsRootsScriptDoesNotAssignReadOnlyHome(t *testing.T) {
	script := decodePowerShellCommand(t, powerShellCommand("powershell.exe", windowsRootsScript))
	compact := strings.Join(strings.Fields(strings.ToLower(script)), "")
	if strings.Contains(compact, "$home=") {
		t.Fatal("Windows roots script assigns to PowerShell read-only HOME variable")
	}
}

func TestSSHWindowsRootsAndSFTPPathStyles(t *testing.T) {
	for _, prefix := range []string{"", "/"} {
		t.Run(fmt.Sprintf("prefix %q", prefix), func(t *testing.T) {
			executor := &targetExecutor{}
			executor.handle = func(string) (base.ExecResult, error) {
				return targetResult(windowsRootsOutput(`C:\Users\alice`, [2]string{"D", `D:\`}, [2]string{"C", `C:\`}), 0), nil
			}
			resolver := &targetRealPath{home: prefix + "C:/Users/alice"}
			filesystem := &sftpFileSystem{executor: executor, realPath: resolver}
			roots, err := filesystem.Roots(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if roots.PathStyle != base.PathStyleWindows || !roots.Supported || roots.HomePath != `C:\Users\alice` || len(roots.Roots) != 2 || roots.Roots[0].Path != `C:\` || roots.Roots[1].Path != `D:\` {
				t.Fatalf("Windows roots = %+v", roots)
			}
			if got := filesystem.sftpPath(`D:\work\file.txt`); got != prefix+"D:/work/file.txt" {
				t.Fatalf("drive path = %q", got)
			}
			if got := filesystem.sftpPath(`/D:/work`); got != prefix+"D:/work" {
				t.Fatalf("SFTP drive input = %q", got)
			}
			for _, input := range []string{"D:", "D:relative", "relative", "~/file"} {
				if got := filesystem.sftpPath(input); got != input {
					t.Fatalf("non-absolute path %q became %q", input, got)
				}
			}
			for _, input := range []string{`\\server\share\dir`, "//server/share/dir"} {
				if got := filesystem.sftpPath(input); got != `\\server/share/dir` {
					t.Fatalf("UNC path %q = %q", input, got)
				}
			}
			commands := executor.commandList()
			if len(commands) != 1 || !strings.HasPrefix(commands[0], "powershell.exe ") {
				t.Fatalf("probe commands = %v", commands)
			}
			script := decodePowerShellCommand(t, commands[0])
			if !strings.Contains(script, "Get-PSDrive -PSProvider FileSystem") || !strings.Contains(script, "OSVersion.Platform") || !strings.Contains(script, "OutputEncoding=[Text.Encoding]::UTF8") {
				t.Fatalf("probe script = %s", script)
			}
		})
	}
}

func TestSSHWindowsRootsRefreshAndUnsupportedPaths(t *testing.T) {
	executor := &targetExecutor{}
	executor.handle = func(string) (base.ExecResult, error) {
		roots := [][2]string{{"C", `C:\`}}
		if len(executor.commandList()) > 1 {
			roots = append(roots, [2]string{"D", `D:\`})
		}
		return targetResult(windowsRootsOutput(`C:\Users\alice`, roots...), 0), nil
	}
	resolver := &targetRealPath{home: "C:/Users/alice"}
	filesystem := &sftpFileSystem{executor: executor, realPath: resolver}
	first, err := filesystem.Roots(context.Background())
	if err != nil || len(first.Roots) != 1 {
		t.Fatalf("first roots = %+v, %v", first, err)
	}
	second, err := filesystem.Roots(context.Background())
	if err != nil || len(second.Roots) != 2 || second.Roots[1].Path != `D:\` {
		t.Fatalf("refreshed roots = %+v, %v", second, err)
	}
	if len(resolver.calls) != 1 || len(executor.commandList()) != 2 {
		t.Fatalf("refresh probing = realpath %v, commands %v", resolver.calls, executor.commandList())
	}
}

func TestSSHRootsErrorClassificationAndFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		home   string
		code   int
		want   error
	}{
		{name: "unsupported", output: `{"windows":true,"supported":false,"status":"ok","roots":[]}`, code: 0, want: base.ErrUnsupported},
		{name: "permission", output: `{"windows":true,"supported":false,"status":"error","kind":"permission","message":"access denied","roots":[]}`, code: 0, want: fs.ErrPermission},
		{name: "no media", output: `{"windows":true,"supported":false,"status":"error","kind":"no_media","message":"device is not ready","roots":[]}`, code: 0, want: base.ErrNoMedia},
		{name: "drive relative", output: windowsRootsOutput("", [2]string{"D", "D:relative"}), code: 0, want: nil},
		{name: "PowerShell unavailable on Windows drive SFTP", output: "not recognized", home: "C:/Users/alice", code: 127, want: base.ErrUnsupported},
		{name: "PowerShell unavailable on Windows UNC SFTP", output: "not recognized", home: `\\server\share`, code: 127, want: base.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &targetExecutor{result: targetResult(test.output, test.code)}
			filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: test.home}}
			roots, err := filesystem.Roots(context.Background())
			if test.want == nil {
				if err == nil {
					t.Fatalf("invalid roots succeeded: %+v", roots)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("roots = %+v, %v; want %v", roots, err, test.want)
			}
		})
	}

	t.Run("empty roots do not fabricate C", func(t *testing.T) {
		executor := &targetExecutor{result: targetResult(windowsRootsOutput(""), 0)}
		filesystem := &sftpFileSystem{executor: executor}
		roots, err := filesystem.Roots(context.Background())
		if err != nil || !roots.Supported || len(roots.Roots) != 0 {
			t.Fatalf("empty roots = %+v, %v", roots, err)
		}
	})

	t.Run("SFTP-only absolute paths stay POSIX", func(t *testing.T) {
		for _, home := range []string{"/home/alice", "/C:/Users/alice", "//server/share"} {
			t.Run(home, func(t *testing.T) {
				executor := &targetExecutor{result: targetResult("not recognized", 127)}
				filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: home}}
				roots, err := filesystem.Roots(context.Background())
				if err != nil || roots.PathStyle != base.PathStylePOSIX || roots.HomePath != home || len(roots.Roots) != 1 || roots.Roots[0].Path != "/" {
					t.Fatalf("SFTP-only roots = %+v, %v", roots, err)
				}
			})
		}
	})

	t.Run("PowerShell on Unix reports the target", func(t *testing.T) {
		for _, home := range []string{"/home/alice", "/C:/Users/alice", "//server/share"} {
			t.Run(home, func(t *testing.T) {
				executor := &targetExecutor{}
				executor.handle = func(command string) (base.ExecResult, error) {
					if strings.HasPrefix(command, "powershell.exe ") {
						return targetResult("not found", 127), nil
					}
					return targetResult(`{"windows":false,"supported":false,"status":"ok","roots":[]}`, 0), nil
				}
				filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: home}}
				roots, err := filesystem.Roots(context.Background())
				if err != nil || roots.PathStyle != base.PathStylePOSIX || roots.HomePath != home || len(roots.Roots) != 1 || roots.Roots[0].Path != "/" {
					t.Fatalf("Unix pwsh roots = %+v, %v", roots, err)
				}
				commands := executor.commandList()
				if len(commands) != 2 || !strings.HasPrefix(commands[1], "pwsh ") {
					t.Fatalf("probe commands = %v", commands)
				}
			})
		}
	})

	t.Run("exec startup failures are preserved", func(t *testing.T) {
		probeErr := errors.New("start SSH exec: ssh: command failed")
		executor := &targetExecutor{err: probeErr}
		filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/home/alice"}}
		if _, err := filesystem.Roots(context.Background()); !errors.Is(err, probeErr) {
			t.Fatalf("startup error = %v, want original error", err)
		}
		if len(executor.commandList()) != 1 {
			t.Fatalf("startup failure triggered fallback commands = %v", executor.commandList())
		}
	})

	t.Run("transport failures are not unsupported", func(t *testing.T) {
		executor := &targetExecutor{err: errors.New("connection lost")}
		filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/home/alice"}}
		if _, err := filesystem.Roots(context.Background()); err == nil || errors.Is(err, base.ErrUnsupported) || !strings.Contains(err.Error(), "connection lost") {
			t.Fatalf("transport error = %v", err)
		}
	})
}

func TestSSHWindowsRootsProbeFailuresAreNotUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		code    int
		stderr  string
		want    error
		message string
	}{
		{name: "access denied", code: 1, stderr: "Access denied", want: fs.ErrPermission, message: "Access denied"},
		{name: "access denied with 127", code: 127, stderr: "Access denied", want: fs.ErrPermission, message: "Access denied"},
		{name: "script error", code: 1, stderr: "syntax error near unexpected token", message: "syntax error near unexpected token"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			code := testCase.code
			executor := &targetExecutor{result: base.ExecResult{Stderr: testCase.stderr, ExitCode: &code}}
			filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/home/alice"}}
			_, err := filesystem.Roots(context.Background())
			if err == nil || !strings.Contains(err.Error(), testCase.message) || errors.Is(err, base.ErrUnsupported) {
				t.Fatalf("probe error = %v", err)
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("probe error = %v, want %v", err, testCase.want)
			}
			if len(executor.commandList()) != 1 {
				t.Fatalf("probe failure triggered fallback commands = %v", executor.commandList())
			}
		})
	}

	for _, code := range []int{127, 9009} {
		t.Run(fmt.Sprintf("command not found %d", code), func(t *testing.T) {
			executor := &targetExecutor{}
			executor.handle = func(command string) (base.ExecResult, error) {
				if strings.HasPrefix(command, "powershell.exe ") {
					failureCode := code
					return base.ExecResult{Stderr: "powershell.exe is not recognized", ExitCode: &failureCode}, nil
				}
				return targetResult(`{"windows":false,"supported":false,"status":"ok","roots":[]}`, 0), nil
			}
			filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/home/alice"}}
			roots, err := filesystem.Roots(context.Background())
			if err != nil || roots.PathStyle != base.PathStylePOSIX {
				t.Fatalf("command-not-found fallback = %+v, %v", roots, err)
			}
			if len(executor.commandList()) != 2 {
				t.Fatalf("command-not-found commands = %v", executor.commandList())
			}
		})
	}

	t.Run("typed exec unsupported falls back", func(t *testing.T) {
		executor := &targetExecutor{}
		executor.handle = func(command string) (base.ExecResult, error) {
			if strings.HasPrefix(command, "powershell.exe ") {
				return base.ExecResult{}, base.ErrUnsupported
			}
			return targetResult(`{"windows":false,"supported":false,"status":"ok","roots":[]}`, 0), nil
		}
		filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/home/alice"}}
		roots, err := filesystem.Roots(context.Background())
		if err != nil || roots.PathStyle != base.PathStylePOSIX {
			t.Fatalf("typed unsupported fallback = %+v, %v", roots, err)
		}
		if len(executor.commandList()) != 2 {
			t.Fatalf("typed unsupported commands = %v", executor.commandList())
		}
	})
}

func TestSSHSFTPErrorClassification(t *testing.T) {
	if err := normalizeSFTPError(errors.New("SFTP list D:/: The device is not ready")); !errors.Is(err, base.ErrNoMedia) {
		t.Fatalf("no-media error = %v", err)
	}
	if err := normalizeSFTPError(errors.New("SFTP list D:/private: Access is denied")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission error = %v", err)
	}
	notExist := fmt.Errorf("SFTP stat: %w", fs.ErrNotExist)
	if err := normalizeSFTPError(notExist); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, base.ErrNoMedia) {
		t.Fatalf("not-exist error = %v", err)
	}
	posixMissing := fmt.Errorf("SFTP list /tmp/no media: %w", fs.ErrNotExist)
	if err := normalizeSFTPError(posixMissing); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, base.ErrNoMedia) {
		t.Fatalf("POSIX special filename error = %v", err)
	}
	windowsMissing := fmt.Errorf("SFTP list D:/access denied: %w", fs.ErrNotExist)
	if err := normalizeSFTPError(windowsMissing); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Windows special filename error = %v", err)
	}
	windowsPermission := fmt.Errorf("SFTP list D:/no media: %w", fs.ErrPermission)
	if err := normalizeSFTPError(windowsPermission); !errors.Is(err, fs.ErrPermission) || errors.Is(err, base.ErrNoMedia) {
		t.Fatalf("structured permission error = %v", err)
	}
}

func TestSSHWindowsSFTPFileOperationsAndUNC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the virtual Windows SFTP namespace fixture uses POSIX filename semantics")
	}
	client, root := newPathTestSFTPClient(t)
	filesystem := newSFTPFileSystem(client, nil)
	filesystem.target = &sftpEnvironment{pathStyle: base.PathStyleWindows, mapWindowsPaths: true}
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(root, "C:"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, `C:\dir`); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, `C:\dir\file.txt`, []byte("hello"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, `C:\dir\file.txt`, []byte("hello world"), true); err != nil {
		t.Fatal(err)
	}
	data, err := filesystem.ReadFile(ctx, `C:\dir\file.txt.nexterm-bak`, 100)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Windows backup = %q, %v", data, err)
	}
	entries, err := filesystem.List(ctx, `C:\dir`)
	if err != nil || len(entries) != 2 {
		t.Fatalf("Windows list = %+v, %v", entries, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, `C:\dir\`) {
			t.Fatalf("non-round-trip entry path = %q", entry.Path)
		}
		if _, err := filesystem.Size(ctx, entry.Path); err != nil {
			t.Fatalf("Size(%q): %v", entry.Path, err)
		}
	}
	ranged, size, err := filesystem.ReadRange(ctx, `C:\dir\file.txt`, 1, 3)
	if err != nil || string(ranged) != "ell" || size != 11 {
		t.Fatalf("Windows range = %q/%d, %v", ranged, size, err)
	}

	localDir := t.TempDir()
	source := filepath.Join(localDir, "source.bin")
	if err := os.WriteFile(source, []byte("transfer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Upload(ctx, source, `C:\dir\upload.bin`, sshfs.TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(localDir, "destination.bin")
	if _, err := filesystem.Download(ctx, `C:\dir\upload.bin`, destination, sshfs.TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(destination)
	if err != nil || string(data) != "transfer" {
		t.Fatalf("Windows transfer = %q, %v", data, err)
	}
	if _, err := filesystem.Upload(ctx, filepath.Join(localDir, "no media - missing"), `C:\dir\missing`, sshfs.TransferOptions{}); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, base.ErrNoMedia) {
		t.Fatalf("client-local upload error was classified as target media failure: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, `\\server`, "share"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, "//server/share/dir"); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, "//server/share/dir/O'Brien.txt", []byte("UNC"), false); err != nil {
		t.Fatal(err)
	}
	entries, err = filesystem.List(ctx, "//server/share/dir")
	if err != nil || len(entries) != 1 || entries[0].Path != "//server/share/dir/O'Brien.txt" {
		t.Fatalf("UNC list = %+v, %v", entries, err)
	}
	data, err = filesystem.ReadFile(ctx, entries[0].Path, 10)
	if err != nil || string(data) != "UNC" {
		t.Fatalf("UNC round trip = %q, %v", data, err)
	}
	if err := filesystem.Delete(ctx, "//server/share/dir", true); err != nil {
		t.Fatal(err)
	}
	if exists, err := filesystem.Exists(ctx, "//server/share/dir"); err != nil || exists {
		t.Fatalf("UNC delete = %v, %v", exists, err)
	}
}

func TestSSHWindowsArchiveOperationsAreUnsupported(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		drivePrefix string
		remotePath  string
	}{
		{name: "drive without slash", remotePath: `D:\folder\a.tar`},
		{name: "drive with slash", drivePrefix: "/", remotePath: `D:\folder\a.tar`},
		{name: "UNC", drivePrefix: "/", remotePath: "//server/share/a.tar"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _ := newPathTestSFTPClient(t)
			executor := &targetExecutor{result: targetResult("", 0)}
			filesystem := newSFTPFileSystem(client, executor)
			filesystem.target = &sftpEnvironment{pathStyle: base.PathStyleWindows, drivePrefix: testCase.drivePrefix, mapWindowsPaths: true}
			if _, err := filesystem.PackDownload(context.Background(), testCase.remotePath, filepath.Join(t.TempDir(), "out.tar.gz"), sshfs.TransferOptions{}); !errors.Is(err, base.ErrUnsupported) {
				t.Fatalf("PackDownload error = %v, want unsupported", err)
			}
			if _, err := filesystem.Extract(context.Background(), testCase.remotePath); !errors.Is(err, base.ErrUnsupported) {
				t.Fatalf("Extract error = %v, want unsupported", err)
			}
			if commands := executor.commandList(); len(commands) != 0 {
				t.Fatalf("Windows archive commands escaped path validation: %v", commands)
			}
		})
	}
}

func TestSSHPOSIXArchiveOperationsDelegate(t *testing.T) {
	client, _ := newPathTestSFTPClient(t)
	executor := &targetExecutor{}
	filesystem := newSFTPFileSystem(client, executor)
	filesystem.target = &sftpEnvironment{pathStyle: base.PathStylePOSIX}
	packErr := errors.New("stop after pack delegation")
	executor.handle = func(command string) (base.ExecResult, error) {
		if strings.HasPrefix(command, "umask 077 && tar -czf '") {
			return base.ExecResult{}, packErr
		}
		return targetResult("", 0), nil
	}
	destination := filepath.Join(t.TempDir(), "out.tar.gz")
	if _, err := filesystem.PackDownload(context.Background(), "/srv/some dir", destination, sshfs.TransferOptions{}); !errors.Is(err, packErr) {
		t.Fatalf("POSIX PackDownload error = %v, want delegated error", err)
	}
	target, err := filesystem.Extract(context.Background(), "/srv/folder/a.tar")
	if err != nil || target != "/srv/folder/a" {
		t.Fatalf("POSIX Extract = %q, %v", target, err)
	}
	commands := executor.commandList()
	if len(commands) != 2 || !strings.Contains(commands[0], "tar -czf '/tmp/nexterm-pack-") || !strings.Contains(commands[0], "-C '/srv' -- 'some dir'") || !strings.Contains(commands[1], "tar -xf '/srv/folder/a.tar' -C '/srv/folder/a'") {
		t.Fatalf("POSIX archive commands = %v", commands)
	}
	for _, remotePath := range []string{"/tmp/nexterm-pack-test.tar.gz", "/C:/Users/alice", "//server/share/a.tar"} {
		if got := filesystem.sftpPath(remotePath); got != remotePath {
			t.Fatalf("POSIX SFTP path %q became %q", remotePath, got)
		}
	}
}

func TestSSHEmptyRootsRebuildsDriveMapping(t *testing.T) {
	executor := &targetExecutor{}
	executor.handle = func(string) (base.ExecResult, error) {
		if len(executor.commandList()) == 1 {
			return targetResult(windowsRootsOutput(`C:\Users\alice`), 0), nil
		}
		return targetResult(windowsRootsOutput(`C:\Users\alice`, [2]string{"D", `D:\`}), 0), nil
	}
	filesystem := &sftpFileSystem{executor: executor, realPath: &targetRealPath{home: "/C:/Users/alice"}}
	first, err := filesystem.Roots(context.Background())
	if err != nil || len(first.Roots) != 0 {
		t.Fatalf("empty roots = %+v, %v", first, err)
	}
	second, err := filesystem.Roots(context.Background())
	if err != nil || len(second.Roots) != 1 || second.Roots[0].Path != `D:\` {
		t.Fatalf("roots after drive insertion = %+v, %v", second, err)
	}
	if got := filesystem.sftpPath(`D:\work`); got != "/D:/work" {
		t.Fatalf("path after drive insertion = %q, want /D:/work", got)
	}
}

func newPathTestSFTPClient(t *testing.T) (*sftp.Client, string) {
	t.Helper()
	root := t.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	client, err := sftp.NewClientPipe(clientSide, clientSide)
	if err != nil {
		clientSide.Close()
		serverSide.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
		clientSide.Close()
		serverSide.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("SFTP path test server did not stop")
		}
	})
	return client, root
}

func testClientConfig(t *testing.T, server *testSSHServer, auth AuthConfig) Config {
	t.Helper()
	host, port := splitAddress(t, server.listener.Addr().String())
	return Config{
		Host:              host,
		Port:              port,
		User:              "test",
		Auth:              auth,
		HostKeys:          NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    2 * time.Second,
		KeepAliveInterval: -1,
	}
}

func connectTestClient(t *testing.T, server *testSSHServer, auth AuthConfig) *Client {
	t.Helper()
	client, err := Connect(context.Background(), testClientConfig(t, server, auth))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func generateClientKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, gossh.Signer) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return public, private, signer
}

func startEchoListener(t *testing.T, network, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 128)
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				return
			}
			if _, err := conn.Write(buffer[:n]); err != nil {
				return
			}
		}
	}()
	return listener
}

func assertConnEcho(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(message))
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != message {
		t.Fatalf("connection echo = %q, %v", data, err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}
