//go:build unix

package ssh

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

type fakeHost struct {
	mu sync.Mutex

	discovery         string
	fallbackDiscovery string
	windows           bool
	pathBinary        string
	probeVersion      string
	probeVersions     map[string]string
	spawn             func()
	socketPath        string
	running           bool
	runHook           func(context.Context, string) (string, int, error, bool)

	shared       *sharedTarget
	uploadDigest string
	uploadErr    error

	exists              map[string]bool
	digests             map[string]string
	directories         map[string]bool
	directoryKinds      map[string]string
	invalidPrivateBases map[string]bool
	mkdirErrors         map[string]error
	chmodErrors         map[string]error
	existsErrors        map[string]error
	deleteErrors        map[string]error
	commands            []string
	mkdirs              []string
	chmods              map[string]fs.FileMode
	uploads             []string
	localUploads        []string
	renames             [][2]string
	deletions           []deleteCall
}

type spawnProcessHost struct {
	*fakeHost
	path string
}

func (h spawnProcessHost) Run(ctx context.Context, command string, _ time.Duration) (string, int, error) {
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Env = append(os.Environ(), "PATH="+h.path+string(os.PathListSeparator)+os.Getenv("PATH"))
	process.WaitDelay = 100 * time.Millisecond
	output, err := process.CombinedOutput()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return string(output), exitError.ExitCode(), nil
	}
	return string(output), 0, err
}

type zeroExitStatusHost struct{ Host }

func (h zeroExitStatusHost) Run(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	stdout, _, err := h.Host.Run(ctx, command, timeout)
	return stdout, 0, err
}

func TestInspectHelperProcessHandlesMissingProcessWithSuppressedExitStatus(t *testing.T) {
	host := zeroExitStatusHost{Host: spawnProcessHost{fakeHost: &fakeHost{}}}
	_, err := inspectHelperProcess(t.Context(), host, Target{GOOS: "linux"}, filepath.Join(t.TempDir(), "daemon"))
	if !errors.Is(err, errHelperProcessNotFound) {
		t.Fatalf("inspectHelperProcess error = %v; want helper process not found", err)
	}
}

const (
	spawnHelperModeEnv = "NEXTERM_SSH_SPAWN_HELPER_MODE"
	spawnHelperPIDEnv  = "NEXTERM_SSH_SPAWN_HELPER_PID"
)

func TestMain(m *testing.M) {
	if os.Getenv(spawnHelperModeEnv) == "" {
		os.Exit(m.Run())
	}
	os.Exit(runSpawnHelperProcess())
}

func runSpawnHelperProcess() int {
	switch filepath.Base(os.Args[0]) {
	case "setsid":
		if len(os.Args) < 2 {
			return 2
		}
		if _, err := syscall.Setsid(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := syscall.Exec(os.Args[1], os.Args[1:], os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "helper":
		if err := os.WriteFile(os.Getenv(spawnHelperPIDEnv), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(30 * time.Second)
		return 0
	default:
		return 2
	}
}

func TestSpawnDaemonClosesExecOutputWhileHelperKeepsRunning(t *testing.T) {
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "helper.pid")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"setsid", "helper"} {
		if err := os.Symlink(executable, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(spawnHelperModeEnv, "1")
	t.Setenv(spawnHelperPIDEnv, pidPath)
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidPath)
		if err == nil {
			pid, err := strconv.Atoi(string(raw))
			if err == nil {
				process, err := os.FindProcess(pid)
				if err == nil {
					_ = process.Kill()
				}
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stateDir := filepath.Join(directory, "state")
	spawnStarted := time.Now()
	helper := filepath.Join(directory, "helper")
	if err := spawnDaemon(ctx, spawnProcessHost{fakeHost: &fakeHost{}, path: directory}, helper, stateDir); err != nil {
		t.Fatal(err)
	}
	spawnElapsed := time.Since(spawnStarted)
	for {
		if raw, err := os.ReadFile(pidPath); err == nil && len(raw) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			raw, pidErr := os.ReadFile(pidPath)
			helperLog, logErr := os.ReadFile(filepath.Join(stateDir, "helper.log"))
			t.Fatalf("detached helper did not start after %s (spawn %s): pid=%q pidErr=%v helperLog=%q helperLogErr=%v",
				time.Since(spawnStarted), spawnElapsed, raw, pidErr, helperLog, logErr)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestParseSHA256Output(t *testing.T) {
	want := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name string
		out  string
		ok   bool
	}{
		{name: "coreutils", out: want + "  /tmp/helper\n", ok: true},
		{name: "uppercase", out: strings.ToUpper(want) + "  /tmp/helper\n", ok: true},
		{name: "malformed", out: "not-a-digest  /tmp/helper\n"},
		{name: "empty", out: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseSHA256Output(test.out)
			if ok != test.ok {
				t.Fatalf("parseSHA256Output(%q) ok = %v, want %v", test.out, ok, test.ok)
			}
			if test.ok && got != want {
				t.Fatalf("parseSHA256Output(%q) = %q, want %q", test.out, got, want)
			}
		})
	}
}

func TestCompressedHelperSourceRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	want := strings.Repeat("helper-payload-", 32*1024)
	if err := os.WriteFile(path, []byte(want), 0o700); err != nil {
		t.Fatal(err)
	}
	compressedPath, compressed, err := compressedHelperSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if !compressed {
		t.Fatal("compressedHelperSource did not compress a compressible helper")
	}
	defer os.Remove(compressedPath)
	compressedFile, err := os.Open(compressedPath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(compressedFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressedFile.Close(); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("compressed helper round trip changed payload")
	}
}

func TestSpawnDaemonReportsPreparationFailure(t *testing.T) {
	for _, operation := range []string{"mkdir", "chmod"} {
		t.Run(operation, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, operation), []byte("#!/bin/sh\nexit 73\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			err := spawnDaemon(t.Context(), spawnProcessHost{fakeHost: &fakeHost{}, path: directory}, "/missing-helper", filepath.Join(directory, "state"))
			if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "exited with 73") {
				t.Fatalf("spawnDaemon = %v; want preparation failure", err)
			}
		})
	}
}

func TestEnsureDaemonReportsFailedHelperStartup(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "setsid"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	host := spawnProcessHost{fakeHost: &fakeHost{}, path: directory}
	resolver := Resolver{StartTimeout: time.Millisecond}
	_, err := resolver.startOrConnect(t.Context(), host, filepath.Join(directory, "missing-helper"), filepath.Join(directory, "state"), "digest")
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("startOrConnect = %v; want startup readiness failure", err)
	}
}

type deleteCall struct {
	path       string
	directory  bool
	contextErr error
}

// sharedTarget models one remote host reached by several independent SSH
// clients: the daemon socket, the uploaded files, and the spawn state are
// shared while command traces stay per client.
type sharedTarget struct {
	mu               sync.Mutex
	running          bool
	socketPath       string
	files            map[string]string
	dirs             map[string]bool
	singleLevel      bool
	mkdirErrors      map[string]error
	uploads          []string
	renames          [][2]string
	spawns           int
	uploadStarted    chan struct{}
	uploadRelease    chan struct{}
	installContended chan struct{}
	uploadOnce       sync.Once
	contentionOnce   sync.Once
}

func newSharedTarget(socketPath string) *sharedTarget {
	return &sharedTarget{socketPath: socketPath, files: make(map[string]string), dirs: make(map[string]bool)}
}

func (s *sharedTarget) spawn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spawns++
	s.running = true
}

func (s *sharedTarget) state() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.socketPath
}

func (s *sharedTarget) exists(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, file := s.files[path]
	return file || s.dirs[path]
}

func (s *sharedTarget) mkdir(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mkdirErrors[path]; err != nil {
		return err
	}
	if s.dirs[path] {
		if strings.HasSuffix(path, ".install") && s.installContended != nil {
			s.contentionOnce.Do(func() { close(s.installContended) })
		}
		return fs.ErrExist
	}
	if _, exists := s.files[path]; exists {
		return fs.ErrExist
	}
	if s.singleLevel {
		parent := pathpkg.Dir(path)
		if parent != path && parent != "/" && !s.dirs[parent] {
			return &os.PathError{Op: "mkdir", Path: path, Err: fs.ErrNotExist}
		}
	}
	s.dirs[path] = true
	return nil
}

func (s *sharedTarget) upload(path, digest string) {
	s.mu.Lock()
	s.files[path] = digest
	s.uploads = append(s.uploads, path)
	s.mu.Unlock()
	if s.uploadStarted != nil {
		s.uploadOnce.Do(func() { close(s.uploadStarted) })
	}
	if s.uploadRelease != nil {
		<-s.uploadRelease
	}
}

func (s *sharedTarget) rename(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[to] = s.files[from]
	delete(s.files, from)
	s.renames = append(s.renames, [2]string{from, to})
}

func (s *sharedTarget) delete(path string, directory bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if directory && s.dirs[path] {
		delete(s.dirs, path)
		return nil
	}
	if !directory {
		if _, exists := s.files[path]; exists {
			delete(s.files, path)
			return nil
		}
	}
	return fs.ErrNotExist
}

func (s *sharedTarget) digest(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest, ok := s.files[path]
	return digest, ok
}

func (s *sharedTarget) counts() (uploads int, renames int, spawns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads), len(s.renames), s.spawns
}

func (s *sharedTarget) uploadPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uploads...)
}

func (s *sharedTarget) renamePairs() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]string(nil), s.renames...)
}

func (h *fakeHost) Run(ctx context.Context, command string, _ time.Duration) (string, int, error) {
	h.mu.Lock()
	h.commands = append(h.commands, command)
	h.mu.Unlock()
	script := readableFakeCommand(command)
	if h.runHook != nil {
		if stdout, exitCode, err, handled := h.runHook(ctx, script); handled {
			return stdout, exitCode, err
		}
	}
	switch {
	case strings.Contains(script, "nexterm-directory-kind"):
		var kinds strings.Builder
		for _, path := range directoryPathsFromCommand(script) {
			kinds.WriteString(fakeDirectoryKind(h, path))
		}
		return kinds.String(), 0, nil
	case strings.Contains(script, "nexterm-private-base"):
		for base := range h.invalidPrivateBases {
			if strings.Contains(script, quoteShell(base)) {
				return "", 74, nil
			}
		}
		return "ok\n", 0, nil
	case strings.Contains(script, "helper.pid"):
		return "", 73, nil
	case strings.Contains(script, "uname -s"):
		if h.windows {
			return "", 1, nil
		}
		return h.discovery, 0, nil
	case strings.Contains(script, "LocalApplicationData"):
		return h.discovery, 0, nil
	case strings.Contains(script, "GetTempPath"):
		return h.fallbackDiscovery, 0, nil
	case strings.Contains(script, "XDG_RUNTIME_DIR"):
		if h.fallbackDiscovery != "" {
			return h.fallbackDiscovery, 0, nil
		}
		return "\n\n", 0, nil
	case strings.HasPrefix(script, "command -v") || strings.Contains(script, "Get-Command nexterm-helper"):
		if h.pathBinary == "" {
			return "", 0, nil
		}
		return h.pathBinary + "\n", 0, nil
	case strings.Contains(script, "--probe"):
		for binary, version := range h.probeVersions {
			if strings.Contains(script, quoteShell(binary)) {
				if version == "" {
					return "unknown command supervisor-helper\n", 2, nil
				}
				return version + "\n", 0, nil
			}
		}
		if h.probeVersion == "" {
			return "unknown command supervisor-helper\n", 2, nil
		}
		return h.probeVersion + "\n", 0, nil
	case strings.Contains(script, "setsid") || strings.Contains(script, "nohup") || strings.Contains(script, "Start-Process"):
		if h.shared != nil {
			h.shared.spawn()
		}
		if h.spawn != nil {
			h.spawn()
		}
		return "", 0, nil
	default:
		return "", 1, fmt.Errorf("unexpected command %q", command)
	}
}

func readableFakeCommand(command string) string {
	_, encoded, found := strings.Cut(command, " -EncodedCommand ")
	if !found {
		return command
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return command
	}
	var script strings.Builder
	for index := 0; index+1 < len(raw); index += 2 {
		script.WriteRune(rune(uint16(raw[index]) | uint16(raw[index+1])<<8))
	}
	return script.String()
}

// directoryPathsFromCommand extracts the paths of a batched directory-kind probe.
func directoryPathsFromCommand(command string) []string {
	if strings.Contains(command, "-LiteralPath") {
		_, remainder, _ := strings.Cut(command, "@(")
		list, _, _ := strings.Cut(remainder, ")) {")
		var paths []string
		for len(list) > 0 {
			list = strings.TrimLeft(list, ", ")
			if !strings.HasPrefix(list, "'") {
				break
			}
			var path strings.Builder
			list = list[1:]
			for len(list) > 0 {
				if strings.HasPrefix(list, "''") {
					path.WriteByte('\'')
					list = list[2:]
					continue
				}
				if list[0] == '\'' {
					list = list[1:]
					break
				}
				path.WriteByte(list[0])
				list = list[1:]
			}
			paths = append(paths, path.String())
		}
		return paths
	}
	_, remainder, _ := strings.Cut(command, "for d in ")
	list, _, _ := strings.Cut(remainder, "; do ")
	var paths []string
	for len(list) > 0 {
		list = strings.TrimLeft(list, " ")
		var path strings.Builder
		for len(list) > 0 && list[0] != ' ' {
			switch {
			case strings.HasPrefix(list, `'"'"'`):
				path.WriteByte('\'')
				list = list[5:]
			case list[0] == '\'':
				end := strings.IndexByte(list[1:], '\'')
				path.WriteString(list[1 : 1+end])
				list = list[end+2:]
			default:
				path.WriteByte(list[0])
				list = list[1:]
			}
		}
		if path.Len() > 0 {
			paths = append(paths, path.String())
		}
	}
	return paths
}

func fakeDirectoryKind(h *fakeHost, path string) string {
	h.mu.Lock()
	if kind := h.directoryKinds[path]; kind != "" {
		h.mu.Unlock()
		return kind + "\n"
	}
	if h.directories[path] {
		h.mu.Unlock()
		return "directory\n"
	}
	if h.exists[path] {
		h.mu.Unlock()
		return "other\n"
	}
	_, digest := h.digests[path]
	h.mu.Unlock()
	if digest {
		return "other\n"
	}
	if h.shared != nil {
		h.shared.mu.Lock()
		directory := h.shared.dirs[path]
		_, file := h.shared.files[path]
		h.shared.mu.Unlock()
		if directory {
			return "directory\n"
		}
		if file {
			return "other\n"
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "missing\n"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink\n"
	}
	if info.IsDir() {
		return "directory\n"
	}
	return "other\n"
}

func (h *fakeHost) Exists(_ context.Context, path string) (bool, error) {
	h.mu.Lock()
	err := h.existsErrors[path]
	h.mu.Unlock()
	if err != nil {
		return false, err
	}
	if h.shared != nil {
		return h.shared.exists(path), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exists[path] || h.directories[path], nil
}

func (h *fakeHost) Mkdir(_ context.Context, path string) error {
	h.mu.Lock()
	h.mkdirs = append(h.mkdirs, path)
	err := h.mkdirErrors[path]
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if h.shared != nil {
		return h.shared.mkdir(path)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.directories[path] || h.exists[path] {
		return fs.ErrExist
	}
	if h.directories == nil {
		h.directories = make(map[string]bool)
	}
	h.directories[path] = true
	return nil
}

func (h *fakeHost) Chmod(_ context.Context, path string, mode fs.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chmods == nil {
		h.chmods = make(map[string]fs.FileMode)
	}
	h.chmods[path] = mode
	return h.chmodErrors[path]
}

func (h *fakeHost) Upload(_ context.Context, localPath, remotePath string) (int64, error) {
	h.mu.Lock()
	h.uploads = append(h.uploads, remotePath)
	h.localUploads = append(h.localUploads, localPath)
	if h.digests == nil {
		h.digests = make(map[string]string)
	}
	h.digests[remotePath] = h.uploadDigest
	err := h.uploadErr
	h.mu.Unlock()
	if h.shared != nil {
		h.shared.upload(remotePath, h.uploadDigest)
	}
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (h *fakeHost) Rename(_ context.Context, from, to string) error {
	h.mu.Lock()
	h.renames = append(h.renames, [2]string{from, to})
	if digest, ok := h.digests[from]; ok {
		h.digests[to] = digest
		delete(h.digests, from)
	}
	h.mu.Unlock()
	if h.shared != nil {
		h.shared.rename(from, to)
	}
	return nil
}

func (h *fakeHost) Delete(ctx context.Context, path string, directory bool) error {
	h.mu.Lock()
	h.deletions = append(h.deletions, deleteCall{path: path, directory: directory, contextErr: ctx.Err()})
	err := h.deleteErrors[path]
	delete(h.digests, path)
	delete(h.exists, path)
	delete(h.directories, path)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if h.shared != nil {
		return h.shared.delete(path, directory)
	}
	return nil
}

func (h *fakeHost) ChecksumSHA256(_ context.Context, path string) (string, error) {
	if h.shared != nil {
		if digest, ok := h.shared.digest(path); ok {
			return digest, nil
		}
		return "", fmt.Errorf("no digest for %s", path)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if digest, ok := h.digests[path]; ok {
		return digest, nil
	}
	return "", fmt.Errorf("no digest for %s", path)
}

func (h *fakeHost) DialExec(_ context.Context, command string) (net.Conn, error) {
	if !strings.Contains(command, "--bridge") {
		return nil, fmt.Errorf("unexpected dial command %q", command)
	}
	if h.shared != nil {
		running, socketPath := h.shared.state()
		if !running {
			return nil, fmt.Errorf("daemon is not running")
		}
		return (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
	}
	h.mu.Lock()
	running := h.running
	socketPath := h.socketPath
	h.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("daemon is not running")
	}
	return (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
}

func (h *fakeHost) sawCommand(fragment string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, command := range h.commands {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}

func (h *fakeHost) spawnCommand() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, command := range h.commands {
		if strings.Contains(command, "setsid") {
			return command
		}
	}
	return ""
}

type daemonFixture struct {
	supervisor *supervisor.Supervisor
	server     *supervisor.Server
	socketPath string
	stateDir   string
}

// newDaemonFixture starts a real supervisor daemon whose state directory is
// dataRoot/nexterm/daemon, mirroring the layout discovery reports for the
// fake target so the hello state digest matches.
func newDaemonFixture(t *testing.T, dataRoot string) *daemonFixture {
	t.Helper()
	stateDir := filepath.Join(dataRoot, "nexterm", "daemon")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-daemon-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &daemonFixture{supervisor: instance, server: server, socketPath: server.SocketPath(), stateDir: stateDir}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	return fixture
}

// testDiscovery reports dataRoot as the target's data home and the real euid
// so the fixture's state digest matches the one the resolver computes.
func testDiscovery(t *testing.T, dataRoot string) string {
	t.Helper()
	return dataRoot + "\nLinux\n" + runtime.GOARCH + "\n" + strconv.Itoa(os.Geteuid()) + "\n"
}

func testResolver(host *fakeHost, executable string) *Resolver {
	return &Resolver{
		Executable:   executable,
		StartTimeout: 10 * time.Second,
		hostFor:      func(context.Context, *transportssh.Client) (Host, error) { return host, nil },
	}
}

func fakeUploadSource(digest string) UploadSource {
	return UploadSource{Path: "/local/nexterm-helper-linux-" + runtime.GOARCH, GOOS: "linux", GOARCH: runtime.GOARCH, Digest: digest}
}

type fakeDaemonClient struct {
	list     func(context.Context) ([]supervisor.Info, error)
	metadata func(context.Context) (supervisor.RuntimeMetadata, bool, error)
	idle     func(context.Context) error
}

func (c *fakeDaemonClient) List(ctx context.Context) ([]supervisor.Info, error) {
	if c.list != nil {
		return c.list(ctx)
	}
	return nil, nil
}

func (c *fakeDaemonClient) RuntimeMetadata(ctx context.Context) (supervisor.RuntimeMetadata, bool, error) {
	if c.metadata != nil {
		return c.metadata(ctx)
	}
	return supervisor.RuntimeMetadata{}, false, nil
}

func (c *fakeDaemonClient) RequireIdle(ctx context.Context) error {
	if c.idle != nil {
		return c.idle(ctx)
	}
	return nil
}

func provenFakeDaemonClient(digest string) *fakeDaemonClient {
	return &fakeDaemonClient{metadata: func(context.Context) (supervisor.RuntimeMetadata, bool, error) {
		return supervisor.RuntimeMetadata{Version: "test", Commit: "test", Protocol: supervisor.ProtocolVersion, SHA256: digest}, true, nil
	}}
}

func TestEnsureDaemonConnectsToRunningDaemon(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-helper",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		running:      true,
	}
	daemon, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != "/usr/bin/nexterm-helper" {
		t.Fatalf("daemon binary = %q", daemon.Binary)
	}
	if want := dataRoot + "/nexterm/daemon"; daemon.StateDir != want {
		t.Fatalf("daemon state dir = %q; want %q", daemon.StateDir, want)
	}
	if host.sawCommand("setsid") {
		t.Fatal("spawned a daemon although one was already running")
	}
	ids, err := daemon.Provider().ListDurable(context.Background())
	if err != nil || len(ids) != 0 {
		t.Fatalf("ListDurable = %v, %v", ids, err)
	}
}

func TestEnsureDaemonProbesAllPathCandidates(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	const helper = "/usr/local/bin/nexterm-helper"
	const legacy = "/usr/bin/nexterm-helper"
	host := &fakeHost{
		discovery:  testDiscovery(t, dataRoot),
		pathBinary: helper + "\n" + legacy,
		probeVersions: map[string]string{
			helper: "1",
			legacy: strconv.Itoa(supervisor.ProtocolVersion),
		},
		socketPath: fixture.socketPath,
		running:    true,
	}
	daemon, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != legacy {
		t.Fatalf("daemon binary = %q; want later compatible legacy candidate %q", daemon.Binary, legacy)
	}
	if !host.sawCommand(quoteShell(helper)+" "+supervisor.HelperCommand+" --probe") || !host.sawCommand(quoteShell(legacy)+" "+supervisor.HelperCommand+" --probe") {
		t.Fatalf("not every PATH candidate was probed: %v", host.commands)
	}
}

func TestEnsureDaemonStartsStoppedDaemon(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-helper",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	daemon, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	spawn := host.spawnCommand()
	if spawn == "" {
		t.Fatal("daemon was not spawned")
	}
	if !strings.Contains(spawn, "mkdir -p --") || !strings.Contains(spawn, "chmod 700") {
		t.Fatalf("spawn command does not prepare the private state dir: %q", spawn)
	}
	attachment, err := daemon.Provider().Create(context.Background(), base.DurableCreateOptions{
		Command: []string{"/bin/sh", "-c", "echo daemon-42"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	buffer := make([]byte, 64)
	var output []byte
	for !strings.Contains(string(output), "daemon-42") {
		count, err := attachment.Read(buffer)
		if err != nil {
			t.Fatalf("read daemon output: %v (output %q)", err, output)
		}
		output = append(output, buffer[:count]...)
	}
}

func TestEnsureDaemonRejectsIncompatibleProbe(t *testing.T) {
	dataRoot := t.TempDir()
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-helper",
		probeVersion: "1",
	}
	_, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnavailable", err)
	}
}

func TestEnsureDaemonUploadsWhenNoCompatibleBinary(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	target := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		digests:      map[string]string{target: digest},
		uploadDigest: digest,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	source := fakeUploadSource(digest)
	source.Version, source.Commit = "1.2.0", "release-commit"
	resolver.inspect = func(string) (UploadSource, error) { return source, nil }
	var progress []HelperProgress
	resolver.Progress = func(_ context.Context, event HelperProgress) { progress = append(progress, event) }
	daemon, err := resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != target {
		t.Fatalf("daemon binary = %q; want %q", daemon.Binary, target)
	}
	if daemon.Update.Local.Version != source.Version || daemon.Update.Local.Commit != source.Commit || daemon.Update.Local.SHA256 != source.Digest || daemon.Update.Local.Protocol != supervisor.ProtocolVersion {
		t.Fatalf("installed helper lost source metadata: %+v", daemon.Update.Local)
	}
	started := false
	for _, event := range progress {
		if event.Stage == "start" {
			started = true
		}
		if event.Done && event.Stage != "ready" {
			t.Fatalf("helper reported completion before readiness: %+v", event)
		}
	}
	if !started || len(progress) == 0 || progress[len(progress)-1].Stage != "ready" || !progress[len(progress)-1].Done {
		t.Fatalf("helper startup/readiness not reported: %+v", progress)
	}
	if len(host.uploads) != 1 {
		t.Fatalf("uploads = %v; want one staged upload", host.uploads)
	}
	staging := host.uploads[0]
	prefix := dataRoot + "/nexterm/bin/.nexterm-helper-" + digest + "."
	if !strings.HasPrefix(staging, prefix) || !strings.HasSuffix(staging, ".partial") {
		t.Fatalf("staging path %q is not a unique sibling of %q", staging, target)
	}
	if host.chmods[staging] != 0o700 {
		t.Fatalf("staging chmods = %v", host.chmods)
	}
	if len(host.renames) != 1 || host.renames[0] != [2]string{staging, target} {
		t.Fatalf("renames = %v; want %q -> %q", host.renames, staging, target)
	}
	for _, dir := range []string{dataRoot + "/nexterm", dataRoot + "/nexterm/bin"} {
		if host.chmods[dir] != 0o700 {
			t.Fatalf("remote dir %s mode = %v; want 0700", dir, host.chmods[dir])
		}
	}
}

func TestEnsureDaemonUploadDigestMismatch(t *testing.T) {
	dataRoot := t.TempDir()
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	target := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	host := &fakeHost{
		discovery: testDiscovery(t, dataRoot),
		digests:   map[string]string{target: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("EnsureDaemon error = %v; want digest mismatch", err)
	}
}

func TestEnsureDaemonRejectsMismatchedUpload(t *testing.T) {
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(path string) (UploadSource, error) {
		return UploadSource{Path: path, GOOS: "linux", GOARCH: other, Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnsupported", err)
	}
}

func TestEnsureDaemonRejectsDesktopForRemoteOS(t *testing.T) {
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "")
	resolver.HelperExecutable = "/local/nexterm-desktop"
	resolver.inspect = func(path string) (UploadSource, error) {
		return UploadSource{Path: path, GOOS: "darwin", GOARCH: runtime.GOARCH, Digest: strings.Repeat("ab", 32)}, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnsupported", err)
	}
	if len(host.uploads) != 0 {
		t.Fatalf("desktop binary was uploaded to a Linux target: %v", host.localUploads)
	}
}

func TestEnsureDaemonRejectsCGOUpload(t *testing.T) {
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(string) (UploadSource, error) {
		source := fakeUploadSource("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		source.CGO = true
		return source, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnsupported", err)
	}
}

func TestEnsureDaemonRejectsUnsupportedTargetOS(t *testing.T) {
	host := &fakeHost{discovery: t.TempDir() + "\nFreeBSD\namd64\n501\n"}
	_, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnsupported", err)
	}
}

func TestEnsureDaemonUsesCachedUploadWithoutReuploading(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cached := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		running:      true,
		exists:       map[string]bool{cached: true},
		digests:      map[string]string{cached: digest},
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	daemon, err := resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != cached {
		t.Fatalf("daemon binary = %q; want the cached upload %q", daemon.Binary, cached)
	}
	if len(host.uploads) != 0 {
		t.Fatalf("cached binary was re-uploaded: %v", host.uploads)
	}
}

func TestEnsureDaemonAutoInstallDisabledStartsCachedHelper(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	digest := strings.Repeat("ab", 32)
	cached := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		exists:       map[string]bool{cached: true},
		digests:      map[string]string{cached: digest},
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.DisableAutoInstall = true
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != cached || !host.sawCommand("setsid") || len(host.uploads) != 0 {
		t.Fatalf("cached helper daemon=%+v spawn=%q uploads=%v", daemon, host.spawnCommand(), host.uploads)
	}
}

func TestEnsureDaemonInstallsOfflineHelper(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	digest := strings.Repeat("ab", 32)
	helperPath := "/offline/nexterm-helper-linux-" + runtime.GOARCH
	target := dataRoot + "/nexterm/bin/nexterm-helper-" + digest
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		uploadDigest: digest,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.HelperExecutable = helperPath
	resolver.inspect = func(path string) (UploadSource, error) {
		if path != helperPath {
			t.Fatalf("inspected %q instead of the explicit offline helper", path)
		}
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	daemon, err := resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != target {
		t.Fatalf("daemon binary = %q; want content-addressed helper %q", daemon.Binary, target)
	}
	if len(host.localUploads) != 1 || host.localUploads[0] != helperPath {
		t.Fatalf("uploaded local paths = %v; want only %q", host.localUploads, helperPath)
	}
	if len(host.uploads) != 1 {
		t.Fatalf("uploads = %v; want one staged helper", host.uploads)
	}
	staging := host.uploads[0]
	if !strings.HasPrefix(staging, dataRoot+"/nexterm/bin/.nexterm-helper-"+digest+".") || !strings.HasSuffix(staging, ".partial") {
		t.Fatalf("helper staging path = %q", staging)
	}
	if len(host.renames) != 1 || host.renames[0] != [2]string{staging, target} {
		t.Fatalf("renames = %v; want %q -> %q", host.renames, staging, target)
	}
	if !host.sawCommand(quoteShell(staging) + " " + supervisor.HelperCommand + " --probe") {
		t.Fatalf("staged helper was not probed before use: %v", host.commands)
	}
	if _, exists := host.digests[staging]; exists {
		t.Fatal("committed helper left its staging file behind")
	}
	for _, dir := range []string{dataRoot + "/nexterm", dataRoot + "/nexterm/bin"} {
		if host.chmods[dir] != 0o700 {
			t.Fatalf("remote dir %s mode = %v; want 0700", dir, host.chmods[dir])
		}
	}
}

func TestEnsureDaemonFindsTargetHelperBesideDesktop(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	digest := strings.Repeat("cd", 32)
	helperPath := "/local/nexterm-helper-linux-" + runtime.GOARCH
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		uploadDigest: digest,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(path string) (UploadSource, error) {
		if path != helperPath {
			t.Fatalf("inspected %q; want target helper %q", path, helperPath)
		}
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	if _, err := resolver.EnsureDaemon(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(host.localUploads) != 1 || host.localUploads[0] != helperPath {
		t.Fatalf("uploaded local paths = %v; want target helper %q", host.localUploads, helperPath)
	}
}

func TestUploadSourceFindsHelperForInstalledAppName(t *testing.T) {
	executable := "/Applications/NexTerm.app/Contents/MacOS/NexTerm"
	helperPath := filepath.Join(filepath.Dir(executable), "nexterm-helper-linux-arm64")
	resolver := &Resolver{Executable: executable}
	resolver.inspect = func(path string) (UploadSource, error) {
		if path != helperPath {
			t.Fatalf("inspected %q; want installed App sibling %q", path, helperPath)
		}
		return UploadSource{Path: path, GOOS: "linux", GOARCH: "arm64", Digest: strings.Repeat("ab", 32)}, nil
	}
	upload, standalone, err := resolver.uploadSource(targetInfo{goos: "linux", goarch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if !standalone || upload.Path != helperPath {
		t.Fatalf("uploadSource = %+v, %t; want standalone %q", upload, standalone, helperPath)
	}
}

func TestEnsureDaemonAutoInstallDisabled(t *testing.T) {
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.HelperExecutable = "/offline/nexterm-helper"
	resolver.DisableAutoInstall = true
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(strings.Repeat("ef", 32))
		source.Path = path
		return source, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "automatic helper installation is disabled") {
		t.Fatalf("EnsureDaemon error = %v; want explicit disabled installation", err)
	}
	if len(host.uploads) != 0 {
		t.Fatalf("disabled automatic installation uploaded %v", host.uploads)
	}
}

func TestEnsureDaemonAutoInstallDisabledPrecedesDesktopValidation(t *testing.T) {
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.DisableAutoInstall = true
	resolver.inspect = func(path string) (UploadSource, error) {
		if filepath.Base(path) != "nexterm-desktop" {
			return UploadSource{}, fs.ErrNotExist
		}
		return UploadSource{Path: path, GOOS: "darwin", GOARCH: runtime.GOARCH, Digest: strings.Repeat("ab", 32)}, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "automatic helper installation is disabled") {
		t.Fatalf("EnsureDaemon error = %v; want disabled installation to dominate validation", err)
	}
	if errors.Is(err, base.ErrUnsupported) || strings.Contains(err.Error(), "targets darwin") {
		t.Fatalf("disabled installation exposed the local desktop validation error: %v", err)
	}
	if len(host.uploads) != 0 {
		t.Fatalf("disabled automatic installation uploaded %v", host.uploads)
	}
}

func TestEnsureDaemonAutoInstallDisabledKeepsPreinstalledHelper(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-helper",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		running:      true,
	}
	resolver := testResolver(host, "")
	resolver.DisableAutoInstall = true
	daemon, err := resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != "/usr/bin/nexterm-helper" {
		t.Fatalf("daemon binary = %q", daemon.Binary)
	}
}

func TestEnsureDaemonHelperDigestMismatchDoesNotCommit(t *testing.T) {
	dataRoot := t.TempDir()
	digest := strings.Repeat("12", 32)
	host := &fakeHost{discovery: testDiscovery(t, dataRoot), uploadDigest: strings.Repeat("ff", 32)}
	resolver := testResolver(host, "")
	resolver.HelperExecutable = "/offline/nexterm-helper"
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("EnsureDaemon error = %v; want digest mismatch", err)
	}
	if len(host.renames) != 0 {
		t.Fatalf("digest mismatch was committed: %v", host.renames)
	}
	if len(host.deletions) == 0 {
		t.Fatal("digest mismatch left its partial upload behind")
	}
}

func TestEnsureDaemonHelperProbeFailureDoesNotCommit(t *testing.T) {
	dataRoot := t.TempDir()
	digest := strings.Repeat("34", 32)
	host := &fakeHost{discovery: testDiscovery(t, dataRoot), uploadDigest: digest}
	resolver := testResolver(host, "")
	resolver.HelperExecutable = "/offline/nexterm-helper"
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	var transferErr *TransferError
	if !errors.Is(err, durable.ErrUnavailable) || !errors.As(err, &transferErr) || transferErr.Stage != TransferStageProbe {
		t.Fatalf("EnsureDaemon error = %v; want helper probe failure", err)
	}
	if len(host.renames) != 0 {
		t.Fatalf("failed helper probe was committed: %v", host.renames)
	}
	if len(host.deletions) == 0 {
		t.Fatal("failed helper probe left its partial upload behind")
	}
}

func TestEnsureDaemonHelperUploadCancellationCleansPartial(t *testing.T) {
	dataRoot := t.TempDir()
	digest := strings.Repeat("56", 32)
	host := &fakeHost{discovery: testDiscovery(t, dataRoot), uploadDigest: digest, uploadErr: context.Canceled}
	resolver := testResolver(host, "")
	resolver.HelperExecutable = "/offline/nexterm-helper"
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureDaemon error = %v; want canceled upload", err)
	}
	if len(host.renames) != 0 {
		t.Fatalf("canceled upload was committed: %v", host.renames)
	}
	if len(host.uploads) != 1 {
		t.Fatalf("canceled uploads = %v; want one partial path", host.uploads)
	}
	stagingDeleted := false
	for _, deletion := range host.deletions {
		if deletion.path == host.uploads[0] && !deletion.directory && deletion.contextErr == nil {
			stagingDeleted = true
		}
	}
	if !stagingDeleted {
		t.Fatalf("canceled upload did not clean its partial file with a live context: %v", host.deletions)
	}
}

func TestEnsureDaemonConcurrentHelperInstallsUploadOnce(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	shared := newSharedTarget(fixture.socketPath)
	shared.uploadStarted = make(chan struct{})
	shared.uploadRelease = make(chan struct{})
	shared.installContended = make(chan struct{})
	digest := strings.Repeat("78", 32)
	target := dataRoot + "/nexterm/bin/nexterm-helper-" + digest

	const clients = 4
	daemons := make([]*Daemon, clients)
	errs := make([]error, clients)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := 0; index < clients; index++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			start.Wait()
			host := &fakeHost{
				discovery:    testDiscovery(t, dataRoot),
				probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
				shared:       shared,
				uploadDigest: digest,
			}
			resolver := testResolver(host, "")
			resolver.HelperExecutable = "/offline/nexterm-helper"
			resolver.inspect = func(path string) (UploadSource, error) {
				source := fakeUploadSource(digest)
				source.Path = path
				return source, nil
			}
			daemons[index], errs[index] = resolver.EnsureDaemon(context.Background(), nil)
		}(index)
	}
	var releaseOnce sync.Once
	releaseUpload := func() { releaseOnce.Do(func() { close(shared.uploadRelease) }) }
	defer releaseUpload()
	start.Done()
	select {
	case <-shared.uploadStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first helper upload did not start")
	}
	select {
	case <-shared.installContended:
	case <-time.After(5 * time.Second):
		t.Fatal("no concurrent client contended for the helper installation lock")
	}
	releaseUpload()
	done.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", index, err)
		}
		if daemons[index].Binary != target {
			t.Fatalf("daemon %d binary = %q; want %q", index, daemons[index].Binary, target)
		}
	}
	uploads, renames, _ := shared.counts()
	if uploads != 1 || renames != 1 {
		t.Fatalf("concurrent helper installation saw %d uploads and %d renames; want one each", uploads, renames)
	}
}

func TestQuoteShell(t *testing.T) {
	if got := quoteShell("/tmp/a b'c"); got != `'/tmp/a b'"'"'c'` {
		t.Fatalf("quoteShell = %s", got)
	}
}

type discoveryFailureHost struct {
	Host
	mu      sync.Mutex
	calls   int
	err     error
	onFirst func()
}

func (h *discoveryFailureHost) Run(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	if strings.Contains(command, "uname -s") {
		h.mu.Lock()
		h.calls++
		call := h.calls
		err := h.err
		onFirst := h.onFirst
		h.mu.Unlock()
		if call == 1 && onFirst != nil {
			onFirst()
		}
		if err != nil {
			return "", 0, err
		}
	}
	return h.Host.Run(ctx, command, timeout)
}

func (h *discoveryFailureHost) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func retryTestResolver(host Host) *Resolver {
	return &Resolver{
		EnsureTimeout: time.Second,
		RetryBackoff:  0,
		hostFor: func(context.Context, *transportssh.Client) (Host, error) {
			return host, nil
		},
	}
}

func TestEnsureDaemonRetriesAtMostThreeCompleteCycles(t *testing.T) {
	host := &discoveryFailureHost{Host: &fakeHost{}, err: durable.ErrUnavailable}
	var progressMu sync.Mutex
	var events []HelperProgress
	resolver := retryTestResolver(host)
	resolver.Progress = func(_ context.Context, event HelperProgress) {
		progressMu.Lock()
		events = append(events, event)
		progressMu.Unlock()
	}
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("EnsureDaemon error = %v", err)
	}
	if got := host.callCount(); got != 3 {
		t.Fatalf("complete ensure cycles = %d, want 3", got)
	}
	progressMu.Lock()
	defer progressMu.Unlock()
	if len(events) < 4 {
		t.Fatalf("progress events = %+v", events)
	}
	operationID := events[0].OperationID
	if operationID == "" || events[0].Attempt != 1 || events[0].Attempts != 3 || events[0].Deadline.IsZero() {
		t.Fatalf("first progress lacks operation scope: %+v", events[0])
	}
	seenAttempts := make(map[int]bool)
	for _, event := range events {
		if event.OperationID != operationID || event.Attempt < 1 || event.Attempt > 3 || event.Attempts != 3 {
			t.Fatalf("unscoped progress event: %+v", event)
		}
		seenAttempts[event.Attempt] = true
	}
	if len(seenAttempts) != 3 {
		t.Fatalf("progress attempts = %v, want all three", seenAttempts)
	}
	last := events[len(events)-1]
	if !last.Done || last.Stage != "failed" || last.Attempt != 3 || last.Cause == "" {
		t.Fatalf("terminal progress = %+v", last)
	}
}

func TestEnsureDaemonPermanentFailureDoesNotRetry(t *testing.T) {
	host := &discoveryFailureHost{Host: &fakeHost{}, err: base.ErrUnsupported}
	_, err := retryTestResolver(host).EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v", err)
	}
	if got := host.callCount(); got != 1 {
		t.Fatalf("permanent failure cycles = %d, want 1", got)
	}
}

func TestEnsureDaemonCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	host := &discoveryFailureHost{Host: &fakeHost{}, err: durable.ErrUnavailable, onFirst: cancel}
	resolver := retryTestResolver(host)
	resolver.RetryBackoff = time.Hour
	started := time.Now()
	_, err := resolver.EnsureDaemon(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureDaemon cancellation = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if got := host.callCount(); got != 1 {
		t.Fatalf("cycles after cancellation = %d, want 1", got)
	}
}

func TestEnsureDaemonMissingTargetHelperDoesNotUploadMainProgram(t *testing.T) {
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.inspect = func(path string) (UploadSource, error) {
		if filepath.Base(path) != "nexterm-helper-linux-"+runtime.GOARCH {
			t.Fatalf("inspected main program %q after target helper was missing", path)
		}
		return UploadSource{}, fs.ErrNotExist
	}
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, ErrHelperNotFound) || !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("missing target helper error = %v", err)
	}
	if len(host.localUploads) != 0 {
		t.Fatalf("main program was uploaded as a standalone helper: %v", host.localUploads)
	}
}

type helperUpdateHarness struct {
	host       *fakeHost
	resolver   *Resolver
	location   deploymentLocation
	current    daemonConnection
	source     *UploadSource
	oldBinary  string
	newBinary  string
	oldDigest  string
	newDigest  string
	oldClient  *fakeDaemonClient
	newClient  *fakeDaemonClient
	oldRunning bool
	newRunning bool
}

func newHelperUpdateHarness(t *testing.T) *helperUpdateHarness {
	t.Helper()
	h := &helperUpdateHarness{
		oldDigest: strings.Repeat("11", 32),
		newDigest: strings.Repeat("22", 32),
		oldBinary: "/opt/nexterm/nexterm-helper-old",
	}
	target := Target{GOOS: "linux", GOARCH: "amd64", DataRoot: t.TempDir(), Identity: "501"}
	paths, err := target.Paths()
	if err != nil {
		t.Fatal(err)
	}
	h.location = deploymentLocation{target: target, paths: paths}
	h.newBinary = paths.BinDir + "/nexterm-helper-" + h.newDigest
	h.host = &fakeHost{
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		uploadDigest: h.newDigest,
		digests:      map[string]string{h.oldBinary: h.oldDigest},
	}
	h.oldRunning = true
	oldMetadata := supervisor.RuntimeMetadata{Version: "v1", Commit: "old", Protocol: supervisor.ProtocolVersion, SHA256: h.oldDigest}
	newMetadata := supervisor.RuntimeMetadata{Version: "v2", Commit: "new", Protocol: supervisor.ProtocolVersion, SHA256: h.newDigest}
	h.oldClient = provenFakeDaemonClient(h.oldDigest)
	h.oldClient.metadata = func(context.Context) (supervisor.RuntimeMetadata, bool, error) { return oldMetadata, true, nil }
	h.oldClient.list = func(context.Context) ([]supervisor.Info, error) {
		if !h.oldRunning {
			return nil, errors.New("old daemon is not running")
		}
		return nil, nil
	}
	h.newClient = provenFakeDaemonClient(h.newDigest)
	h.newClient.metadata = func(context.Context) (supervisor.RuntimeMetadata, bool, error) { return newMetadata, true, nil }
	h.newClient.list = func(context.Context) ([]supervisor.Info, error) {
		if !h.newRunning {
			return nil, errors.New("new daemon is not running")
		}
		return nil, nil
	}
	h.resolver = &Resolver{StartTimeout: 30 * time.Millisecond, UploadTimeout: time.Second, EnsureTimeout: time.Second}
	h.resolver.clientFor = func(daemon *Daemon) remoteDaemonClient {
		if daemon.Binary == h.newBinary {
			return h.newClient
		}
		return h.oldClient
	}
	h.current = daemonConnection{daemon: newTargetDaemon(h.host, h.location, h.oldBinary), metadata: oldMetadata, known: true, running: true}
	h.source = &UploadSource{Path: "/offline/nexterm-helper", GOOS: "linux", GOARCH: "amd64", Digest: h.newDigest, Version: "v2", Commit: "new"}
	h.host.runHook = func(_ context.Context, command string) (string, int, error, bool) {
		switch {
		case strings.Contains(command, "helper.pid") && (strings.Contains(command, "/proc/") || strings.Contains(command, "ps -p")):
			if h.newRunning {
				return "4343\n" + h.newBinary + "\n", 0, nil, true
			}
			if h.oldRunning {
				return "4242\n" + h.oldBinary + "\n", 0, nil, true
			}
			return "", 73, nil, true
		case command == "kill -TERM 4242":
			h.oldRunning = false
			return "", 0, nil, true
		case command == "kill -TERM 4343":
			h.newRunning = false
			return "", 0, nil, true
		case strings.Contains(command, "setsid"):
			if strings.Contains(command, quoteShell(h.newBinary)) {
				h.newRunning = true
			} else if strings.Contains(command, quoteShell(h.oldBinary)) {
				h.oldRunning = true
			}
			return "", 0, nil, true
		default:
			return "", 0, nil, false
		}
	}
	return h
}

func (h *helperUpdateHarness) update(ctx context.Context, interrupt bool) (*Daemon, error) {
	return h.resolver.updateRunningDaemon(ctx, h.host, h.location, h.source, h.current, EnsureOptions{InterruptActive: interrupt})
}

func (h *helperUpdateHarness) ensure(ctx context.Context, interrupt bool) (*Daemon, error) {
	h.host.discovery = h.location.target.DataRoot + "\nLinux\namd64\n501\n"
	h.resolver.HelperExecutable = h.source.Path
	h.resolver.RetryBackoff = -1
	h.resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) { return h.host, nil }
	h.resolver.inspect = func(string) (UploadSource, error) { return *h.source, nil }
	return h.resolver.EnsureDaemonWithOptions(ctx, nil, EnsureOptions{InterruptActive: interrupt})
}

func TestEnsureDaemonCompatibleReleaseDifferenceContinues(t *testing.T) {
	h := newHelperUpdateHarness(t)
	h.source.Digest = h.oldDigest
	h.host.uploadDigest = h.oldDigest
	h.host.discovery = h.location.target.DataRoot + "\nLinux\namd64\n501\n"
	h.host.pathBinary = h.oldBinary
	h.resolver.HelperExecutable = h.source.Path
	h.resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) { return h.host, nil }
	h.resolver.inspect = func(string) (UploadSource, error) { return *h.source, nil }
	daemon, err := h.resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != h.oldBinary || daemon.Update.Updated || len(h.host.uploads) != 0 {
		t.Fatalf("compatible release difference triggered update: daemon=%+v uploads=%v", daemon, h.host.uploads)
	}
	if !h.oldRunning || h.host.sawCommand("kill -TERM") {
		t.Fatal("compatible running daemon was interrupted for a release-only difference")
	}
	if daemon.Update.Local.Version != "v2" || daemon.Update.Running.Version != "v1" || daemon.Update.Local.Protocol != daemon.Update.Running.Protocol {
		t.Fatalf("local/running compatibility evidence = %+v", daemon.Update)
	}
}

func TestEnsureDaemonWithOptionsDefersDiscoveredContentAddressedUpdate(t *testing.T) {
	h := newHelperUpdateHarness(t)
	h.oldClient.idle = func(context.Context) error { return supervisor.ErrActiveSessions }
	daemon, err := h.ensure(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != h.oldBinary || !daemon.Update.Deferred || !h.oldRunning || h.newRunning {
		t.Fatalf("active update was not deferred: daemon=%+v old=%v new=%v", daemon, h.oldRunning, h.newRunning)
	}
	if h.host.sawCommand("kill -TERM") {
		t.Fatal("active processes were interrupted without explicit permission")
	}
	if digest := h.host.digests[h.newBinary]; digest != h.newDigest {
		t.Fatalf("candidate was not installed before deferral: %q", digest)
	}
	if digest := h.host.digests[h.oldBinary]; digest != h.oldDigest {
		t.Fatalf("old executable was not preserved: %q", digest)
	}
}

func TestEnsureDaemonWithOptionsInterruptsDiscoveredContentAddressedUpdate(t *testing.T) {
	h := newHelperUpdateHarness(t)
	h.oldClient.idle = func(context.Context) error { return supervisor.ErrActiveSessions }
	daemon, err := h.ensure(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !daemon.Update.Updated || !daemon.Update.Interrupted || daemon.Binary != h.newBinary || !h.newRunning || h.oldRunning {
		t.Fatalf("explicit update = %+v old=%v new=%v", daemon, h.oldRunning, h.newRunning)
	}
	if !h.host.sawCommand("kill -TERM 4242") {
		t.Fatal("actual old helper process was not stopped")
	}
	if digest := h.host.digests[h.oldBinary]; digest != h.oldDigest {
		t.Fatalf("old executable was removed during switch: %q", digest)
	}
}

func TestEnsureDaemonWithOptionsRollsBackDiscoveredContentAddressedUpdate(t *testing.T) {
	h := newHelperUpdateHarness(t)
	h.newClient.list = func(context.Context) ([]supervisor.Info, error) { return nil, errors.New("new daemon not ready") }
	daemon, err := h.ensure(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "without reviving interrupted shells") {
		t.Fatalf("rollback error = %v", err)
	}
	if daemon == nil || !daemon.Update.RolledBack {
		t.Fatalf("rollback result was not preserved: daemon=%+v", daemon)
	}
	if !h.oldRunning || h.newRunning {
		t.Fatalf("rollback process state old=%v new=%v", h.oldRunning, h.newRunning)
	}
	if digest := h.host.digests[h.oldBinary]; digest != h.oldDigest {
		t.Fatalf("rollback executable was not preserved: %q", digest)
	}
}

func TestHelperUpdateRollbackUsesDefaultTimeout(t *testing.T) {
	h := newHelperUpdateHarness(t)
	h.resolver.StartTimeout = 0
	h.newClient.metadata = func(context.Context) (supervisor.RuntimeMetadata, bool, error) {
		return supervisor.RuntimeMetadata{Version: "v2", Commit: "new", Protocol: supervisor.ProtocolVersion, SHA256: h.oldDigest}, true, nil
	}
	daemon, err := h.update(t.Context(), false)
	if err == nil || daemon == nil || !daemon.Update.RolledBack || !h.oldRunning || h.newRunning {
		t.Fatalf("default rollback daemon=%+v err=%v old=%v new=%v", daemon, err, h.oldRunning, h.newRunning)
	}
}

func TestHelperUpdateDoesNotTreatTransportLossAsStopped(t *testing.T) {
	h := newHelperUpdateHarness(t)
	originalHook := h.host.runHook
	h.host.runHook = func(ctx context.Context, command string) (string, int, error, bool) {
		if !h.oldRunning && strings.Contains(command, "helper.pid") {
			return "", 0, errors.New("SSH process inspection disconnected"), true
		}
		return originalHook(ctx, command)
	}
	_, err := h.update(t.Context(), false)
	if err == nil || h.newRunning {
		t.Fatalf("transport loss was treated as a stopped daemon: err=%v new=%v", err, h.newRunning)
	}
}

func TestHelperUpdateRollbackAndCleanupFailuresAreVisible(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		h := newHelperUpdateHarness(t)
		h.newClient.list = func(context.Context) ([]supervisor.Info, error) { return nil, errors.New("new daemon not ready") }
		originalHook := h.host.runHook
		h.host.runHook = func(ctx context.Context, command string) (string, int, error, bool) {
			if strings.Contains(command, "setsid") && strings.Contains(command, quoteShell(h.oldBinary)) {
				return "", 0, nil, true
			}
			return originalHook(ctx, command)
		}
		_, err := h.update(t.Context(), false)
		if err == nil || !strings.Contains(err.Error(), "rollback failed") {
			t.Fatalf("rollback failure = %v", err)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		h := newHelperUpdateHarness(t)
		lock := h.location.paths.Root + "/.helper-update"
		h.host.deleteErrors = map[string]error{lock: errors.New("cleanup failed")}
		daemon, err := h.update(t.Context(), false)
		if err == nil || !strings.Contains(err.Error(), "cleanup failed") || daemon == nil {
			t.Fatalf("cleanup failure daemon=%+v error=%v", daemon, err)
		}
	})
}

func TestEnsureDaemonDrivesAllNineStandaloneTargets(t *testing.T) {
	for _, target := range SupportedTargets() {
		t.Run(target.ID(), func(t *testing.T) {
			target.DataRoot = filepath.Join(t.TempDir(), "data")
			target.Identity = "501"
			host := &fakeHost{probeVersion: strconv.Itoa(supervisor.ProtocolVersion), uploadDigest: strings.Repeat("ab", 32)}
			machine := target.GOARCH
			if target.GOARCH == "arm" {
				machine = "armv7l"
			}
			osName := "Linux"
			if target.GOOS == "darwin" {
				osName = "Darwin"
			}
			host.discovery = target.DataRoot + "\n" + osName + "\n" + machine + "\n" + target.Identity + "\n"
			if target.GOOS == "windows" {
				target.DataRoot = `C:\Users\test\AppData\Local`
				target.Identity = "S-1-5-21-42"
				host.windows = true
				architecture := "X64"
				if target.GOARCH == "arm64" {
					architecture = "Arm64"
				}
				host.discovery = fmt.Sprintf(`{"data_root":%q,"architecture":%q,"identity":%q}`, target.DataRoot, architecture, target.Identity)
				host.directoryKinds = map[string]string{`C:\`: "directory"}
			}
			host.spawn = func() {
				host.mu.Lock()
				host.running = true
				host.mu.Unlock()
			}
			resolver := testResolver(host, "/local/nexterm-desktop")
			resolver.inspect = func(path string) (UploadSource, error) {
				name := "nexterm-helper-" + target.ID()
				if target.GOOS == "windows" {
					name += ".exe"
				}
				if want := filepath.Join("/local", name); path != want {
					t.Fatalf("inspected %q, want target package %q", path, want)
				}
				return UploadSource{Path: path, GOOS: target.GOOS, GOARCH: target.GOARCH, GOARM: target.GOARM, Digest: host.uploadDigest}, nil
			}
			resolver.clientFor = func(*Daemon) remoteDaemonClient {
				return &fakeDaemonClient{list: func(context.Context) ([]supervisor.Info, error) {
					host.mu.Lock()
					running := host.running
					host.mu.Unlock()
					if !running {
						return nil, errors.New("daemon is not running")
					}
					return nil, nil
				}}
			}
			daemon, err := resolver.EnsureDaemon(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := target.Paths()
			if err != nil {
				t.Fatal(err)
			}
			wantBinary, err := paths.Helper(host.uploadDigest)
			if err != nil {
				t.Fatal(err)
			}
			if daemon.Binary != wantBinary || daemon.StateDir != paths.StateDir || daemon.Target.ID() != target.ID() {
				t.Fatalf("daemon = %+v, want binary=%q state=%q target=%s", daemon, wantBinary, paths.StateDir, target.ID())
			}
			if len(host.uploads) != 1 || len(host.localUploads) != 1 || !host.running {
				t.Fatalf("upload/start evidence missing: uploads=%v local=%v running=%v", host.uploads, host.localUploads, host.running)
			}
		})
	}
}

func temporaryFallbackHarness(t *testing.T, uid string) (*fakeHost, *Resolver, Target, TargetPaths) {
	t.Helper()
	target := Target{GOOS: "linux", GOARCH: runtime.GOARCH, DataRoot: filepath.Join(t.TempDir(), "data"), Identity: uid}
	paths, err := target.Paths()
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("cd", 32)
	host := &fakeHost{
		discovery:    target.DataRoot + "\nLinux\n" + runtime.GOARCH + "\n" + uid + "\n",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		uploadDigest: digest,
		mkdirErrors:  map[string]error{paths.Root: fs.ErrPermission},
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm-desktop")
	resolver.RetryBackoff = -1
	resolver.EnsureTimeout = time.Second
	resolver.inspect = func(path string) (UploadSource, error) {
		source := fakeUploadSource(digest)
		source.Path = path
		return source, nil
	}
	resolver.clientFor = func(*Daemon) remoteDaemonClient {
		return &fakeDaemonClient{list: func(context.Context) ([]supervisor.Info, error) {
			host.mu.Lock()
			running := host.running
			host.mu.Unlock()
			if !running {
				return nil, errors.New("daemon is not running")
			}
			return nil, nil
		}}
	}
	return host, resolver, target, paths
}

func TestEnsureDaemonUsesPrivateTemporaryFallbackAndReportsLifecycle(t *testing.T) {
	for _, uid := range []string{"501", "502"} {
		t.Run("uid "+uid, func(t *testing.T) {
			host, resolver, _, _ := temporaryFallbackHarness(t, uid)
			var events []HelperProgress
			resolver.Progress = func(_ context.Context, event HelperProgress) { events = append(events, event) }
			daemon, err := resolver.EnsureDaemon(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			wantRoot := "/tmp/nexterm-" + uid + "/nexterm"
			if !daemon.Temporary || daemon.StateDir != wantRoot+"/daemon" {
				t.Fatalf("fallback daemon = %+v, want state under %s", daemon, wantRoot)
			}
			var fallback *HelperProgress
			for index := range events {
				if events[index].Stage == "fallback" {
					fallback = &events[index]
				}
			}
			if fallback == nil || fallback.Path != wantRoot || !strings.Contains(fallback.Cause, "cleanup") {
				t.Fatalf("temporary path or cleanup risk is not visible: %+v", events)
			}
			if fallback.Error != "" {
				t.Fatalf("successful fallback was reported as an error: %+v", fallback)
			}
			if len(host.uploads) != 1 || !host.running {
				t.Fatalf("fallback upload/start missing: uploads=%v running=%v", host.uploads, host.running)
			}
		})
	}
}

func TestEnsureDaemonRejectsUnsafeTemporaryRoot(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	base := "/tmp/nexterm-501"
	host.invalidPrivateBases = map[string]bool{base: true}
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, errUnsafeTemporaryRoot) {
		t.Fatalf("unsafe temporary root error = %v", err)
	}
	if host.running || len(host.uploads) != 0 {
		t.Fatalf("unsafe root was used: running=%v uploads=%v", host.running, host.uploads)
	}
}

func TestEnsureDaemonRejectsUnsafeTemporaryPathWithoutUpload(t *testing.T) {
	for _, reason := range []string{"symlink", "wrong owner", "wrong mode"} {
		t.Run(reason, func(t *testing.T) {
			host, resolver, _, _ := temporaryFallbackHarness(t, "501")
			base := "/tmp/nexterm-501"
			host.pathBinary = "/usr/bin/nexterm-helper"
			host.invalidPrivateBases = map[string]bool{base: true}
			host.directories = map[string]bool{base: true}
			var events []HelperProgress
			resolver.Progress = func(_ context.Context, event HelperProgress) { events = append(events, event) }
			daemon, err := resolver.EnsureDaemon(t.Context(), nil)
			if !errors.Is(err, errUnsafeTemporaryRoot) || daemon != nil {
				t.Fatalf("unsafe PATH-only start daemon=%+v error=%v", daemon, err)
			}
			if host.running || len(host.uploads) != 0 || host.sawCommand("setsid") {
				t.Fatalf("unsafe PATH-only start ran helper: running=%v uploads=%v commands=%v", host.running, host.uploads, host.commands)
			}
			visible := false
			for _, event := range events {
				if event.Stage == "fallback" && event.Path == base+"/nexterm" && strings.Contains(event.Cause, "cleanup") {
					visible = true
				}
			}
			if !visible {
				t.Fatalf("temporary path and cleanup risk are not visible: %+v", events)
			}
		})
	}
}

func TestEnsureDaemonDisableAutoInstallValidatesTemporaryCache(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	base := "/tmp/nexterm-501"
	cached := base + "/nexterm/bin/nexterm-helper-" + host.uploadDigest
	host.invalidPrivateBases = map[string]bool{base: true}
	host.directories = map[string]bool{base: true}
	host.exists = map[string]bool{cached: true}
	host.digests = map[string]string{cached: host.uploadDigest}
	resolver.DisableAutoInstall = true
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, errUnsafeTemporaryRoot) || daemon != nil {
		t.Fatalf("unsafe cached start daemon=%+v error=%v", daemon, err)
	}
	if host.running || len(host.uploads) != 0 || host.sawCommand("setsid") {
		t.Fatalf("unsafe cached helper started: running=%v uploads=%v commands=%v", host.running, host.uploads, host.commands)
	}
}

func TestEnsureDaemonRejectsTemporaryRootSymlinkBeforePathStart(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	base := "/tmp/nexterm-501"
	root := base + "/nexterm"
	host.pathBinary = "/usr/bin/nexterm-helper"
	host.directories = map[string]bool{base: true}
	host.directoryKinds = map[string]string{root: "symlink", root + "/bin": "directory", root + "/daemon": "directory"}
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, errUnsafeTemporaryRoot) || daemon != nil {
		t.Fatalf("root symlink PATH-only start daemon=%+v error=%v", daemon, err)
	}
	if host.running || len(host.uploads) != 0 || host.sawCommand("setsid") {
		t.Fatalf("root symlink was used: running=%v uploads=%v commands=%v", host.running, host.uploads, host.commands)
	}
}

func TestEnsureDaemonRejectsTemporaryBinSymlinkBeforeCachedStart(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	base := "/tmp/nexterm-501"
	root := base + "/nexterm"
	cached := root + "/bin/nexterm-helper-" + host.uploadDigest
	host.directories = map[string]bool{base: true}
	host.directoryKinds = map[string]string{root: "directory", root + "/bin": "symlink", root + "/daemon": "directory"}
	host.exists = map[string]bool{cached: true}
	host.digests = map[string]string{cached: host.uploadDigest}
	resolver.DisableAutoInstall = true
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, errUnsafeTemporaryRoot) || daemon != nil {
		t.Fatalf("bin symlink cached start daemon=%+v error=%v", daemon, err)
	}
	if host.running || len(host.uploads) != 0 || host.sawCommand("setsid") {
		t.Fatalf("bin symlink was used: running=%v uploads=%v commands=%v", host.running, host.uploads, host.commands)
	}
}

func TestEnsureDaemonSkipsUnsafeXDGForPrivateTemporaryRoot(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	xdg := t.TempDir()
	host.fallbackDiscovery = xdg + "\n\n"
	host.invalidPrivateBases = map[string]bool{xdg: true}
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.StateDir != "/tmp/nexterm-501/nexterm/daemon" {
		t.Fatalf("daemon state = %q, want safe per-uid temporary root", daemon.StateDir)
	}
	for _, uploaded := range host.uploads {
		if strings.HasPrefix(uploaded, xdg) {
			t.Fatalf("unsafe XDG path received upload %q", uploaded)
		}
	}
}

func TestEnsureDaemonDoesNotMoveExistingStateToFallback(t *testing.T) {
	t.Run("primary state exists", func(t *testing.T) {
		host, resolver, _, paths := temporaryFallbackHarness(t, "501")
		host.directories = map[string]bool{paths.StateDir: true}
		_, err := resolver.EnsureDaemon(t.Context(), nil)
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("existing primary state error = %v", err)
		}
		if host.running || len(host.uploads) != 0 {
			t.Fatalf("existing state was silently moved: running=%v uploads=%v", host.running, host.uploads)
		}
	})
	t.Run("primary state suppresses fallback daemon", func(t *testing.T) {
		host, resolver, _, paths := temporaryFallbackHarness(t, "501")
		host.directories = map[string]bool{paths.StateDir: true}
		host.pathBinary = "/usr/bin/nexterm-helper"
		resolver.StartTimeout = 20 * time.Millisecond
		resolver.clientFor = func(daemon *Daemon) remoteDaemonClient {
			return &fakeDaemonClient{list: func(context.Context) ([]supervisor.Info, error) {
				if daemon.StateDir == paths.StateDir {
					return nil, errors.New("primary daemon is stopped")
				}
				return nil, nil
			}}
		}
		daemon, err := resolver.EnsureDaemon(t.Context(), nil)
		if err == nil {
			t.Fatalf("fallback daemon silently replaced primary recovery: %+v", daemon)
		}
	})
	t.Run("fallback state remains selected", func(t *testing.T) {
		host, resolver, _, paths := temporaryFallbackHarness(t, "501")
		fallbackState := "/tmp/nexterm-501/nexterm/daemon"
		host.directories = map[string]bool{fallbackState: true}
		daemon, err := resolver.EnsureDaemon(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if daemon.StateDir != fallbackState {
			t.Fatalf("daemon state = %q, want previous fallback state", daemon.StateDir)
		}
		for _, directory := range host.mkdirs {
			if directory == paths.Root {
				t.Fatal("primary root was used despite existing fallback state")
			}
		}
	})
}

func TestEnsureDaemonRedeploysAfterTemporaryCleanup(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	first, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	host.running = false
	delete(host.digests, first.Binary)
	delete(host.directories, first.StateDir)
	host.mu.Unlock()
	second, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.StateDir != first.StateDir || len(host.uploads) != 2 {
		t.Fatalf("temporary cleanup redeployment first=%+v second=%+v uploads=%v", first, second, host.uploads)
	}
}

func TestEnsureDaemonReportsNoexecFallbackWithoutStarting(t *testing.T) {
	host, resolver, _, _ := temporaryFallbackHarness(t, "501")
	host.runHook = func(_ context.Context, command string) (string, int, error, bool) {
		if strings.Contains(command, "--probe") {
			return "", 126, nil, true
		}
		return "", 0, nil, false
	}
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, ErrNoExec) {
		t.Fatalf("noexec error = %v", err)
	}
	if host.running {
		t.Fatal("daemon started after noexec probe failure")
	}
}

type sftpUnavailableHost struct {
	Host
	uploads int
}

func (h *sftpUnavailableHost) Exists(context.Context, string) (bool, error) {
	return false, ErrSFTPUnavailable
}
func (h *sftpUnavailableHost) Mkdir(context.Context, string) error { return ErrSFTPUnavailable }
func (h *sftpUnavailableHost) Chmod(context.Context, string, fs.FileMode) error {
	return ErrSFTPUnavailable
}
func (h *sftpUnavailableHost) Upload(context.Context, string, string) (int64, error) {
	h.uploads++
	return 0, ErrSFTPUnavailable
}
func (h *sftpUnavailableHost) Rename(context.Context, string, string) error {
	return ErrSFTPUnavailable
}
func (h *sftpUnavailableHost) Delete(context.Context, string, bool) error {
	return ErrSFTPUnavailable
}
func (h *sftpUnavailableHost) ChecksumSHA256(context.Context, string) (string, error) {
	return "", ErrSFTPUnavailable
}

func TestEnsureDaemonUsesExecFallbackWhenSFTPUnavailable(t *testing.T) {
	worker, resolver, _, paths := temporaryFallbackHarness(t, "501")
	worker.mkdirErrors = nil
	primary := &sftpUnavailableHost{Host: worker}
	resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) {
		return NewFallbackHost(primary, worker), nil
	}
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.StateDir != paths.StateDir || primary.uploads != 1 || len(worker.uploads) != 1 {
		t.Fatalf("SFTP/exec ensure daemon=%+v primary attempts=%d exec uploads=%v", daemon, primary.uploads, worker.uploads)
	}
}

type thirdCycleHost struct {
	Host
	calls int
}

func (h *thirdCycleHost) Run(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	if strings.Contains(command, "uname -s") {
		h.calls++
		if h.calls <= 2 {
			return "", 0, durable.ErrUnavailable
		}
	}
	return h.Host.Run(ctx, command, timeout)
}

func TestEnsureDaemonSucceedsOnThirdCompleteCycle(t *testing.T) {
	worker, resolver, _, _ := temporaryFallbackHarness(t, "501")
	worker.mkdirErrors = nil
	host := &thirdCycleHost{Host: worker}
	resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) { return host, nil }
	daemon, err := resolver.EnsureDaemon(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon == nil || host.calls != 3 || !worker.running {
		t.Fatalf("third-cycle success daemon=%+v calls=%d running=%v", daemon, host.calls, worker.running)
	}
}

type blockingUploadHost struct {
	*fakeHost
	uploads int
}

func (h *blockingUploadHost) Upload(ctx context.Context, _, _ string) (int64, error) {
	h.uploads++
	<-ctx.Done()
	return 0, context.Cause(ctx)
}

func TestEnsureDaemonSharedBudgetCoversFallbackUpload(t *testing.T) {
	worker, resolver, _, _ := temporaryFallbackHarness(t, "501")
	worker.mkdirErrors = nil
	blocking := &blockingUploadHost{fakeHost: worker}
	primary := &sftpUnavailableHost{Host: blocking}
	resolver.hostFor = func(context.Context, *transportssh.Client) (Host, error) {
		return NewFallbackHost(primary, blocking), nil
	}
	resolver.EnsureTimeout = 40 * time.Millisecond
	resolver.UploadTimeout = time.Minute
	started := time.Now()
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fallback upload deadline = %v", err)
	}
	if time.Since(started) > time.Second || primary.uploads != 1 || blocking.uploads != 1 {
		t.Fatalf("shared upload budget elapsed=%s primary=%d fallback=%d", time.Since(started), primary.uploads, blocking.uploads)
	}
	if worker.running {
		t.Fatal("daemon started after shared upload budget expired")
	}
}

func TestEnsureDaemonSharedBudgetCoversStartup(t *testing.T) {
	worker, resolver, _, _ := temporaryFallbackHarness(t, "501")
	worker.mkdirErrors = nil
	resolver.clientFor = func(*Daemon) remoteDaemonClient {
		return &fakeDaemonClient{list: func(context.Context) ([]supervisor.Info, error) {
			return nil, errors.New("daemon not ready")
		}}
	}
	resolver.EnsureTimeout = 40 * time.Millisecond
	resolver.StartTimeout = time.Minute
	_, err := resolver.EnsureDaemon(t.Context(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup deadline = %v", err)
	}
	if !worker.running || !worker.sawCommand("setsid") {
		t.Fatal("startup was not attempted before the shared deadline")
	}
}

func TestHelperUpdateRechecksProtocolAndStateBeforeSwitch(t *testing.T) {
	t.Run("protocol metadata changed", func(t *testing.T) {
		h := newHelperUpdateHarness(t)
		h.oldClient.metadata = func(context.Context) (supervisor.RuntimeMetadata, bool, error) {
			return supervisor.RuntimeMetadata{Version: "v1", Commit: "old", Protocol: supervisor.ProtocolVersion, SHA256: h.newDigest}, true, nil
		}
		_, err := h.update(t.Context(), false)
		if !errors.Is(err, ErrProtocol) || !h.oldRunning || h.newRunning || h.host.sawCommand("kill -TERM") {
			t.Fatalf("protocol recheck error=%v old=%v new=%v", err, h.oldRunning, h.newRunning)
		}
	})
	t.Run("state changed", func(t *testing.T) {
		h := newHelperUpdateHarness(t)
		h.oldClient.list = func(context.Context) ([]supervisor.Info, error) { return nil, supervisor.ErrStateMismatch }
		_, err := h.update(t.Context(), false)
		if !errors.Is(err, supervisor.ErrStateMismatch) || !h.oldRunning || h.newRunning || h.host.sawCommand("kill -TERM") {
			t.Fatalf("state recheck error=%v old=%v new=%v", err, h.oldRunning, h.newRunning)
		}
	})
}
