package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type transferDelete struct {
	path       string
	directory  bool
	contextErr error
}

type transferFakeHost struct {
	mu sync.Mutex

	sftpErr       error
	uploadErr     error
	deleteErr     error
	lockDeleteErr error
	probeOutput   string
	probeExit     int
	probeErr      error
	uploadDigest  string

	files    map[string]string
	dirs     map[string]bool
	uploads  []string
	renames  [][2]string
	deletes  []transferDelete
	commands []string
}

func newTransferFakeHost() *transferFakeHost {
	return &transferFakeHost{files: make(map[string]string), dirs: make(map[string]bool), probeOutput: strconv.Itoa(supervisor.ProtocolVersion)}
}

func (h *transferFakeHost) Run(_ context.Context, command string, _ time.Duration) (string, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, command)
	return h.probeOutput, h.probeExit, h.probeErr
}

func (h *transferFakeHost) Exists(_ context.Context, path string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sftpErr != nil {
		return false, h.sftpErr
	}
	_, file := h.files[path]
	return file || h.dirs[path], nil
}

func (h *transferFakeHost) Mkdir(_ context.Context, path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sftpErr != nil {
		return h.sftpErr
	}
	if h.dirs[path] {
		return fs.ErrExist
	}
	h.dirs[path] = true
	return nil
}

func (h *transferFakeHost) Chmod(_ context.Context, _ string, _ fs.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sftpErr
}

func (h *transferFakeHost) Upload(_ context.Context, _, remotePath string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.uploads = append(h.uploads, remotePath)
	if h.sftpErr != nil {
		return 0, h.sftpErr
	}
	if h.uploadErr != nil {
		return 0, h.uploadErr
	}
	h.files[remotePath] = h.uploadDigest
	return 1, nil
}

func (h *transferFakeHost) UploadWithProgress(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	transferred, err := h.Upload(ctx, localPath, remotePath)
	if options.Progress != nil {
		options.Progress(sshfs.Progress{Transferred: transferred, Total: transferred, Done: err == nil})
	}
	return transferred, err
}

func (h *transferFakeHost) Rename(_ context.Context, from, to string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sftpErr != nil {
		return h.sftpErr
	}
	h.renames = append(h.renames, [2]string{from, to})
	h.files[to] = h.files[from]
	delete(h.files, from)
	return nil
}

func (h *transferFakeHost) Delete(ctx context.Context, path string, directory bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deletes = append(h.deletes, transferDelete{path: path, directory: directory, contextErr: ctx.Err()})
	if directory && h.lockDeleteErr != nil {
		return h.lockDeleteErr
	}
	if !directory && h.deleteErr != nil {
		return h.deleteErr
	}
	delete(h.files, path)
	delete(h.dirs, path)
	return nil
}

func (h *transferFakeHost) ChecksumSHA256(_ context.Context, path string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sftpErr != nil {
		return "", h.sftpErr
	}
	digest, ok := h.files[path]
	if !ok {
		return "", fs.ErrNotExist
	}
	return digest, nil
}

func (h *transferFakeHost) DialExec(context.Context, string) (net.Conn, error) {
	return nil, base.ErrUnsupported
}

func TestFallbackHostOnlyRetriesUnavailableSFTP(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{name: "typed unavailable", err: ErrSFTPUnavailable, fallback: true},
		{name: "OpenSSH subsystem rejection", err: fmt.Errorf("request SFTP subsystem: %w", errors.New("ssh: subsystem request failed")), fallback: true},
		{name: "permission", err: fs.ErrPermission},
		{name: "noexec", err: ErrNoExec},
		{name: "protocol", err: ErrProtocol},
		{name: "trust", err: ErrTrust},
		{name: "cancellation", err: context.Canceled},
		{name: "disconnect", err: base.ErrDisconnected},
		{name: "wrapped EOF", err: fmt.Errorf("request SFTP subsystem: %w", io.EOF)},
		{name: "wrapped network closed", err: fmt.Errorf("request SFTP subsystem: %w", net.ErrClosed)},
		{name: "subsystem negotiation reset", err: errors.New("request SFTP subsystem: connection reset by peer")},
		{name: "generic failure", err: errors.New("SFTP write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := newTransferFakeHost()
			primary.uploadErr = test.err
			fallback := newTransferFakeHost()
			host := NewFallbackHost(primary, fallback)
			_, err := host.Upload(context.Background(), "local", "remote")
			if test.fallback {
				if err != nil {
					t.Fatalf("fallback upload = %v", err)
				}
				if len(fallback.uploads) != 1 {
					t.Fatalf("fallback uploads = %v", fallback.uploads)
				}
				return
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("primary error = %v, want %v", err, test.err)
			}
			if len(fallback.uploads) != 0 {
				t.Fatalf("non-capability failure triggered fallback: %v", fallback.uploads)
			}
			var stageErr *TransferError
			if !errors.As(err, &stageErr) || stageErr.Stage != TransferStageSFTP {
				t.Fatalf("primary error lacks SFTP stage: %v", err)
			}
		})
	}
}

func TestFallbackHostUploadSwitchesWhenFileNotVisibleToExec(t *testing.T) {
	primary := newTransferFakeHost()
	primary.uploadDigest = "digest"
	execView := newTransferFakeHost()
	execView.uploadDigest = "digest"
	host := NewFallbackHost(primary, execView)
	if _, err := host.Upload(t.Context(), "local", "remote"); err != nil {
		t.Fatal(err)
	}
	if len(primary.uploads) != 1 || len(primary.deletes) != 1 {
		t.Fatalf("split SFTP upload was not cleaned up: uploads=%v deletions=%v", primary.uploads, primary.deletes)
	}
	if _, exists := primary.files["remote"]; exists {
		t.Fatal("invisible SFTP upload remained after cleanup")
	}
	if len(execView.uploads) != 1 || execView.files["remote"] != "digest" {
		t.Fatalf("exec upload = uploads %v, files %v", execView.uploads, execView.files)
	}
	if _, err := host.Upload(t.Context(), "local", "second"); err != nil {
		t.Fatal(err)
	}
	if len(primary.uploads) != 1 || len(execView.uploads) != 2 {
		t.Fatalf("exec fallback was not sticky: primary=%v exec=%v", primary.uploads, execView.uploads)
	}
}

func TestFallbackHostUploadAllowsMissingInvisiblePrimaryCleanup(t *testing.T) {
	primary := newTransferFakeHost()
	primary.deleteErr = fs.ErrNotExist
	execView := newTransferFakeHost()
	host := NewFallbackHost(primary, execView)
	if _, err := host.Upload(t.Context(), "local", "remote"); err != nil {
		t.Fatal(err)
	}
	if len(execView.uploads) != 1 {
		t.Fatalf("exec uploads = %v, want one after an already-missing primary upload", execView.uploads)
	}
}

func TestParseExecExistenceHandlesSuppressedExitStatus(t *testing.T) {
	path := "/root/.local/share/nexterm/bin/helper"
	stdout := "stat: cannot statx '" + path + "': No such file or directory\n"
	exists, err := parseExecExistence(Target{GOOS: "linux"}, path, stdout)
	if err != nil || exists {
		t.Fatalf("missing helper = exists %t, error %v", exists, err)
	}
	for _, unexpected := range []string{"", "stat: cannot statx '/another/path': No such file or directory\n", "stat: permission denied\n"} {
		if _, err := parseExecExistence(Target{GOOS: "linux"}, path, unexpected); !errors.Is(err, ErrProtocol) {
			t.Fatalf("unexpected existence output %q produced error %v; want protocol failure", unexpected, err)
		}
	}
}

type fakeExecOpener struct {
	channel *fakeExecChannel
	command string
	err     error
	calls   int
}

func (o *fakeExecOpener) OpenExec(_ context.Context, command string, _ base.ExecOptions) (base.Channel, error) {
	o.calls++
	o.command = command
	if o.err != nil {
		return nil, o.err
	}
	return o.channel, nil
}

type fakeExecChannel struct {
	mu          sync.Mutex
	input       bytes.Buffer
	stderr      *strings.Reader
	waitErr     error
	closeWrites int
	waitCalls   int
	closed      bool
}

func (c *fakeExecChannel) Read([]byte) (int, error) { return 0, io.EOF }
func (c *fakeExecChannel) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.closeWrites > 0 {
		return 0, net.ErrClosed
	}
	return c.input.Write(p)
}
func (c *fakeExecChannel) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}
func (c *fakeExecChannel) Stderr() io.Reader {
	if c.stderr == nil {
		return strings.NewReader("")
	}
	return c.stderr
}
func (c *fakeExecChannel) Resize(context.Context, uint32, uint32) error { return base.ErrUnsupported }
func (c *fakeExecChannel) Wait(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waitCalls++
	if c.closeWrites == 0 {
		return errors.New("Wait called before CloseWrite")
	}
	return c.waitErr
}
func (c *fakeExecChannel) CloseWrite() error {
	c.mu.Lock()
	c.closeWrites++
	c.mu.Unlock()
	return nil
}
func (c *fakeExecChannel) ID() string         { return "fake-exec" }
func (c *fakeExecChannel) Generation() uint64 { return 1 }

func TestUploadExecStreamsStdinHalfClosesAndWaits(t *testing.T) {
	path := t.TempDir() + "/helper"
	payload := []byte("binary\x00payload\nwith lines")
	if err := writeTestFile(path, payload); err != nil {
		t.Fatal(err)
	}
	channel := &fakeExecChannel{}
	opener := &fakeExecOpener{channel: channel}
	target := Target{GOOS: "linux", GOARCH: "amd64"}
	transferred, err := UploadExec(context.Background(), opener, target, path, "/tmp/helper partial", sshfs.TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transferred != int64(len(payload)) || !bytes.Equal(channel.input.Bytes(), payload) {
		t.Fatalf("transfer = %d/%q, want %d/%q", transferred, channel.input.Bytes(), len(payload), payload)
	}
	if channel.closeWrites != 1 || channel.waitCalls != 1 {
		t.Fatalf("half-close/wait calls = %d/%d", channel.closeWrites, channel.waitCalls)
	}
	if strings.Contains(opener.command, "curl") || strings.Contains(opener.command, "wget") || strings.Contains(opener.command, "base64") {
		t.Fatalf("exec upload uses a forbidden downloader or encoding: %q", opener.command)
	}
}

func TestUploadExecWindowsUsesBinaryStandardInput(t *testing.T) {
	path := t.TempDir() + "/helper"
	payload := []byte("windows\x00binary")
	if err := writeTestFile(path, payload); err != nil {
		t.Fatal(err)
	}
	channel := &fakeExecChannel{}
	opener := &fakeExecOpener{channel: channel}
	_, err := UploadExec(context.Background(), opener, Target{GOOS: "windows", GOARCH: "arm64"}, path, `C:\NexTerm\helper.partial.exe`, sshfs.TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	script := decodePowerShellScript(t, opener.command)
	if !strings.Contains(script, "OpenStandardInput") || !strings.Contains(script, "CopyTo") || strings.Contains(script, "Get-Content") {
		t.Fatalf("Windows stdin script = %s", script)
	}
	if !bytes.Equal(channel.input.Bytes(), payload) || channel.closeWrites != 1 || channel.waitCalls != 1 {
		t.Fatalf("Windows transfer did not preserve bytes and half-close: %q, %d/%d", channel.input.Bytes(), channel.closeWrites, channel.waitCalls)
	}
}

func TestUploadExecPreservesExitStatusAndStderr(t *testing.T) {
	path := t.TempDir() + "/helper"
	if err := writeTestFile(path, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	channel := &fakeExecChannel{waitErr: &base.ExitError{Code: 23}, stderr: strings.NewReader("disk write failed")}
	_, err := UploadExec(context.Background(), &fakeExecOpener{channel: channel}, Target{GOOS: "linux", GOARCH: "amd64"}, path, "/tmp/helper", sshfs.TransferOptions{})
	if code, ok := base.ExitCode(err); !ok || code != 23 {
		t.Fatalf("upload error = %v, want exit 23", err)
	}
	if !strings.Contains(err.Error(), "disk write failed") {
		t.Fatalf("upload error lacks remote stderr: %v", err)
	}
	var stageErr *TransferError
	if !errors.As(err, &stageErr) || stageErr.Stage != TransferStageWait {
		t.Fatalf("upload error lacks wait stage: %v", err)
	}
}

func TestUploadExecRejectsMissingExitStatus(t *testing.T) {
	path := t.TempDir() + "/helper"
	if err := writeTestFile(path, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	channel := &fakeExecChannel{waitErr: base.ErrExitStatusMissing}
	_, err := UploadExec(context.Background(), &fakeExecOpener{channel: channel}, Target{GOOS: "linux", GOARCH: "amd64"}, path, "/tmp/helper", sshfs.TransferOptions{})
	if !errors.Is(err, base.ErrExitStatusMissing) || !hasTransferStage(err, TransferStageWait) {
		t.Fatalf("missing exit status = %v", err)
	}
}

func TestUploadExecCancellationDoesNotOpenChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opener := &fakeExecOpener{channel: &fakeExecChannel{}}
	if _, err := UploadExec(ctx, opener, Target{GOOS: "linux", GOARCH: "amd64"}, "unused", "/tmp/helper", sshfs.TransferOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload = %v", err)
	}
	if opener.calls != 0 {
		t.Fatal("canceled upload opened a channel")
	}
}

func TestStageUploadDigestMismatchAndPartialCleanupFailureAreVisible(t *testing.T) {
	host := newTransferFakeHost()
	host.uploadDigest = strings.Repeat("ff", 32)
	cleanupErr := errors.New("partial cleanup failed")
	host.deleteErr = cleanupErr
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: "/data"}
	source := UploadSource{Path: "/local/helper", GOOS: "linux", GOARCH: "amd64", Digest: strings.Repeat("ab", 32)}
	_, err := StageUpload(context.Background(), host, target, source, true, nil)
	if !errors.Is(err, ErrDigestMismatch) || !errors.Is(err, cleanupErr) {
		t.Fatalf("StageUpload error = %v, want digest mismatch and cleanup cause", err)
	}
	if !hasTransferStage(err, TransferStageCleanupPartial) {
		t.Fatalf("StageUpload error lacks partial cleanup stage: %v", err)
	}
	if len(host.renames) != 0 {
		t.Fatalf("digest mismatch was committed: %v", host.renames)
	}
}

func TestStageUploadCancellationCleansWithLiveContext(t *testing.T) {
	host := newTransferFakeHost()
	host.uploadErr = context.Canceled
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: "/data"}
	source := UploadSource{Path: "/local/helper", GOOS: "linux", GOARCH: "amd64", Digest: strings.Repeat("ab", 32)}
	_, err := StageUpload(context.Background(), host, target, source, true, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StageUpload cancellation = %v", err)
	}
	var partialCleaned bool
	for _, deletion := range host.deletes {
		if !deletion.directory && strings.HasSuffix(deletion.path, ".partial") && deletion.contextErr == nil {
			partialCleaned = true
		}
	}
	if !partialCleaned {
		t.Fatalf("cancellation did not clean partial with live context: %+v", host.deletes)
	}
}

func TestStageUploadLockCleanupFailureIsVisible(t *testing.T) {
	host := newTransferFakeHost()
	host.uploadDigest = strings.Repeat("ab", 32)
	cleanupErr := errors.New("lock cleanup failed")
	host.lockDeleteErr = cleanupErr
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: "/data"}
	source := UploadSource{Path: "/local/helper", GOOS: "linux", GOARCH: "amd64", Digest: host.uploadDigest}
	_, err := StageUpload(context.Background(), host, target, source, true, nil)
	if !errors.Is(err, cleanupErr) || !hasTransferStage(err, TransferStageCleanupLock) {
		t.Fatalf("StageUpload lock cleanup = %v", err)
	}
	if len(host.renames) != 1 {
		t.Fatalf("successful upload did not commit before reporting cleanup: %v", host.renames)
	}
}

func TestStageUploadProbeFailuresDoNotCommit(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		exit   int
		want   error
	}{
		{name: "protocol", output: "1", want: ErrProtocol},
		{name: "noexec", exit: 126, want: ErrNoExec},
		{name: "permission", exit: 126, want: fs.ErrPermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := newTransferFakeHost()
			host.uploadDigest = strings.Repeat("ab", 32)
			host.probeOutput = test.output
			host.probeExit = test.exit
			if test.name == "noexec" {
				host.probeOutput = "noexec mount"
			} else if test.name == "permission" {
				host.probeOutput = "permission denied"
			}
			target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: "/data"}
			source := UploadSource{Path: "/local/helper", GOOS: "linux", GOARCH: "amd64", Digest: host.uploadDigest}
			_, err := StageUpload(context.Background(), host, target, source, true, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("probe error = %v, want %v", err, test.want)
			}
			if len(host.renames) != 0 {
				t.Fatalf("failed probe was committed: %v", host.renames)
			}
		})
	}
}

func hasTransferStage(err error, stage TransferStage) bool {
	if err == nil {
		return false
	}
	var stageErr *TransferError
	if errors.As(err, &stageErr) && stageErr.Stage == stage {
		return true
	}
	switch value := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range value.Unwrap() {
			if hasTransferStage(child, stage) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasTransferStage(value.Unwrap(), stage)
	}
	return false
}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o700)
}

type execTestSSHServer struct {
	listener net.Listener
	hostKey  gossh.PublicKey
	config   *gossh.ServerConfig
	pty      atomic.Int32
	sftp     atomic.Int32
}

func newExecTestSSHServer(t *testing.T) *execTestSSHServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &gossh.ServerConfig{PasswordCallback: func(metadata gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
		if metadata.User() == "test" && string(password) == "secret" {
			return nil, nil
		}
		return nil, errors.New("invalid test credentials")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &execTestSSHServer{listener: listener, hostKey: signer.PublicKey(), config: config}
	go server.accept()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *execTestSSHServer) accept() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConnection(connection)
	}
}

func (s *execTestSSHServer) handleConnection(connection net.Conn) {
	serverConnection, channels, requests, err := gossh.NewServerConn(connection, s.config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer serverConnection.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err == nil {
			go s.handleSession(channel, channelRequests)
		}
	}
}

func (s *execTestSSHServer) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()
	for request := range requests {
		switch request.Type {
		case "pty-req":
			s.pty.Add(1)
			_ = request.Reply(false, nil)
		case "subsystem":
			s.sftp.Add(1)
			_ = request.Reply(false, nil)
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(request.Payload, &payload); err != nil {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			s.execute(channel, payload.Command)
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func (s *execTestSSHServer) execute(channel gossh.Channel, command string) {
	process := exec.Command("/bin/sh", "-c", command)
	if strings.HasPrefix(command, "umask 077 && cat > ") {
		process.Stdin = channel
	} else {
		process.Stdin = strings.NewReader("")
	}
	process.Stdout = channel
	process.Stderr = channel.Stderr()
	err := process.Run()
	status := uint32(0)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = uint32(exitErr.ExitCode())
		} else {
			_, _ = channel.Stderr().Write([]byte(err.Error()))
			status = 127
		}
	}
	_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: status}))
}

func (s *execTestSSHServer) connect(t *testing.T) *transportssh.Client {
	t.Helper()
	host, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := transportssh.Connect(context.Background(), transportssh.Config{
		Host: host, Port: port, User: "test",
		Auth:              transportssh.AuthConfig{Method: transportssh.AuthPassword, Password: "secret"},
		HostKeys:          transportssh.NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    3 * time.Second,
		KeepAliveInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestStageUploadOverInProcessSSHWhenSFTPIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the in-process shell fixture uses /bin/sh")
	}
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("the in-process fixture asserts the host's supported target only")
	}
	server := newExecTestSSHServer(t)
	client := server.connect(t)
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, DataRoot: filepath.Join(t.TempDir(), "target root")}
	if err := os.MkdirAll(target.DataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	execHost, err := NewExecHost(client, target)
	if err != nil {
		t.Fatal(err)
	}
	primary := &clientHost{client: client}
	host := NewFallbackHost(primary, execHost)

	local := filepath.Join(t.TempDir(), "helper")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = %q ] && [ \"$2\" = --probe ]; then printf '%%s\\n' %q; exit 0; fi\nexit 2\n", supervisor.HelperCommand, strconv.Itoa(supervisor.ProtocolVersion))
	if err := os.WriteFile(local, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	source := UploadSource{Path: local, GOOS: target.GOOS, GOARCH: target.GOARCH, Digest: contentSHA256(t, local)}
	installed, err := StageUpload(context.Background(), host, target, source, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	installedBytes, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(installedBytes) != script {
		t.Fatal("installed helper content differs from the offline source")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(installed), "*.partial"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("partial files remain: %v, %v", matches, err)
	}
	if server.pty.Load() != 0 {
		t.Fatalf("fallback requested %d PTYs", server.pty.Load())
	}
	if server.sftp.Load() == 0 {
		t.Fatal("test did not exercise the rejected SFTP subsystem")
	}
}

func TestRemoteCommandNotFoundRejectsLoaderAndTransportErrors(t *testing.T) {
	for _, code := range []int{126, 127} {
		err := fmt.Errorf("stat loader failed: no such file or directory: %w", &base.ExitError{Code: code})
		if remoteCommandNotFound(err) {
			t.Fatalf("loader exit %d was folded into target-not-found", code)
		}
	}
	if remoteCommandNotFound(errors.New("OpenExec transport failed: no such file or directory")) {
		t.Fatal("non-ExitError transport failure was folded into target-not-found")
	}
}

func TestExecHostExistsDistinguishesPermissionFromMissing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged POSIX user")
	}
	server := newExecTestSSHServer(t)
	client := server.connect(t)
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, DataRoot: t.TempDir()}
	execHost, err := NewExecHost(client, target)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(target.DataRoot, "nexterm", "daemon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target.DataRoot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target.DataRoot, 0o700) })
	exists, err := execHost.Exists(context.Background(), stateDir)
	if err == nil || exists {
		t.Fatalf("permission-denied state = exists %v, err %v; want unknown", exists, err)
	}
	if err := os.Chmod(target.DataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	exists, err = execHost.Exists(context.Background(), stateDir)
	if err != nil || !exists {
		t.Fatalf("existing state = exists %v, err %v", exists, err)
	}
	exists, err = execHost.Exists(context.Background(), stateDir+"-missing")
	if err != nil || exists {
		t.Fatalf("definitely missing state = exists %v, err %v", exists, err)
	}
}

func TestEnsureDaemonExecFallbackDoesNotMoveUnprovableState(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged POSIX user")
	}
	dataRoot := t.TempDir()
	fallbackBase := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("HOME", dataRoot)
	if runtime.GOOS == "darwin" {
		t.Setenv("TMPDIR", fallbackBase)
	} else {
		t.Setenv("XDG_RUNTIME_DIR", fallbackBase)
	}
	stateDir := filepath.Join(dataRoot, "nexterm", "daemon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataRoot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataRoot, 0o700) })

	server := newExecTestSSHServer(t)
	client := server.connect(t)
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, DataRoot: dataRoot, Identity: strconv.Itoa(os.Geteuid())}
	execHost, err := NewExecHost(client, target)
	if err != nil {
		t.Fatal(err)
	}
	host := NewFallbackHost(&clientHost{client: client}, execHost)
	local := filepath.Join(t.TempDir(), "helper")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = %q ] && [ \"$2\" = --probe ]; then printf '%%s\\n' %q; exit 0; fi\nexit 2\n", supervisor.HelperCommand, strconv.Itoa(supervisor.ProtocolVersion))
	if err := os.WriteFile(local, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{
		HelperExecutable: local,
		RetryBackoff:     -1,
		hostFor: func(context.Context, *transportssh.Client) (Host, error) {
			return host, nil
		},
	}
	resolver.inspect = func(path string) (UploadSource, error) {
		return UploadSource{Path: path, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Digest: contentSHA256(t, local)}, nil
	}
	_, err = resolver.EnsureDaemonWithOptions(context.Background(), client, EnsureOptions{})
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("EnsureDaemonWithOptions error = %v; want preserved permission uncertainty", err)
	}
	if _, statErr := os.Stat(filepath.Join(fallbackBase, "nexterm")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("unprovable original state caused fallback deployment: %v", statErr)
	}
	if mode, statErr := os.Stat(dataRoot); statErr != nil || mode.Mode().Perm() != 0 {
		t.Fatalf("primary path was changed before proving state: mode=%v err=%v", mode.Mode().Perm(), statErr)
	}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return client, nil
		}),
		DurableResolver: resolver,
	})
	defer func() { _ = manager.Close() }()
	connected, err := manager.Connect(context.Background(), session.Asset{ID: "ssh-unprovable-state", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.RecoverRemoteDurable(context.Background(), "0123456789abcdef0123456789abcdef", session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "recover", Cols: 80, Rows: 24,
	})
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; want original uncertainty, not tab-not-found", err)
	}
}

func TestEnsureDaemonExecFallbackPreservesStatLoaderFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged POSIX user")
	}
	dataRoot := t.TempDir()
	fallbackBase := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("HOME", dataRoot)
	if runtime.GOOS == "darwin" {
		t.Setenv("TMPDIR", fallbackBase)
	} else {
		t.Setenv("XDG_RUNTIME_DIR", fallbackBase)
	}
	stateDir := filepath.Join(dataRoot, "nexterm", "daemon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataRoot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataRoot, 0o700) })
	realStat, err := exec.LookPath("stat")
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	diagnostic := "stat: cannot statx '" + stateDir + "': No such file or directory"
	statScript := fmt.Sprintf("#!/bin/sh\nlast=\nfor arg in \"$@\"; do last=$arg; done\nif [ \"$last\" = %s ]; then printf '%%s\\n' %s >&2; exit 127; fi\nexec %s \"$@\"\n", quoteShell(stateDir), quoteShell(diagnostic), quoteShell(realStat))
	if err := os.WriteFile(filepath.Join(fakeBin, "stat"), []byte(statScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	server := newExecTestSSHServer(t)
	client := server.connect(t)
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, DataRoot: dataRoot, Identity: strconv.Itoa(os.Geteuid())}
	execHost, err := NewExecHost(client, target)
	if err != nil {
		t.Fatal(err)
	}
	exists, existsErr := execHost.Exists(context.Background(), stateDir)
	if code, ok := base.ExitCode(existsErr); exists || !ok || code != 127 {
		t.Fatalf("direct stat probe = exists %v, error %v (code %d, ok %v)", exists, existsErr, code, ok)
	}
	host := NewFallbackHost(&clientHost{client: client}, execHost)
	local := filepath.Join(t.TempDir(), "helper")
	helperScript := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = %q ] && [ \"$2\" = --probe ]; then printf '%%s\\n' %q; exit 0; fi\nexit 2\n", supervisor.HelperCommand, strconv.Itoa(supervisor.ProtocolVersion))
	if err := os.WriteFile(local, []byte(helperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{
		HelperExecutable: local,
		RetryBackoff:     -1,
		hostFor: func(context.Context, *transportssh.Client) (Host, error) {
			return host, nil
		},
	}
	resolver.inspect = func(path string) (UploadSource, error) {
		return UploadSource{Path: path, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Digest: contentSHA256(t, local)}, nil
	}
	_, err = resolver.EnsureDaemonWithOptions(context.Background(), client, EnsureOptions{})
	if code, ok := base.ExitCode(err); !ok || code != 127 {
		t.Fatalf("EnsureDaemonWithOptions error = %v; want preserved stat exit 127", err)
	}
	if _, statErr := os.Stat(filepath.Join(fallbackBase, "nexterm")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("stat loader failure caused fallback deployment: %v", statErr)
	}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return client, nil
		}),
		DurableResolver: resolver,
	})
	defer func() { _ = manager.Close() }()
	connected, err := manager.Connect(context.Background(), session.Asset{ID: "ssh-stat-loader-state", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.RecoverRemoteDurable(context.Background(), "0123456789abcdef0123456789abcdef", session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "recover", Cols: 80, Rows: 24,
	})
	if code, ok := base.ExitCode(err); !ok || code != 127 || errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("RecoverRemoteDurable error = %v; want stat exit 127, not tab-not-found", err)
	}
}
