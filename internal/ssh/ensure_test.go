//go:build unix

package ssh

import (
	"compress/gzip"
	"context"
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
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

type fakeHost struct {
	mu sync.Mutex

	discovery     string
	pathBinary    string
	probeVersion  string
	probeVersions map[string]string
	spawn         func()
	socketPath    string
	running       bool

	shared       *sharedTarget
	uploadDigest string
	uploadErr    error

	exists       map[string]bool
	digests      map[string]string
	commands     []string
	mkdirs       []string
	chmods       map[string]fs.FileMode
	uploads      []string
	localUploads []string
	renames      [][2]string
	deletions    []deleteCall
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

func TestSpawnDaemonClosesExecOutputWhileHelperKeepsRunning(t *testing.T) {
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "helper.pid")
	if err := os.WriteFile(filepath.Join(directory, "setsid"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(directory, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s' \"$$\" > "+quoteShell(pidPath)+"\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
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
	if err := spawnDaemon(ctx, spawnProcessHost{fakeHost: &fakeHost{}, path: directory}, helper, filepath.Join(directory, "state")); err != nil {
		t.Fatal(err)
	}
	for {
		if raw, err := os.ReadFile(pidPath); err == nil && len(raw) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("detached helper did not start")
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
	if s.dirs[path] {
		if strings.HasSuffix(path, ".install") && s.installContended != nil {
			s.contentionOnce.Do(func() { close(s.installContended) })
		}
		return fs.ErrExist
	}
	if _, exists := s.files[path]; exists {
		return fs.ErrExist
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

func (h *fakeHost) Run(_ context.Context, command string, _ time.Duration) (string, int, error) {
	h.mu.Lock()
	h.commands = append(h.commands, command)
	h.mu.Unlock()
	switch {
	case strings.HasPrefix(command, "printf"):
		return h.discovery, 0, nil
	case strings.HasPrefix(command, "command -v"):
		if h.pathBinary == "" {
			return "", 0, nil
		}
		return h.pathBinary + "\n", 0, nil
	case strings.Contains(command, "--probe"):
		for binary, version := range h.probeVersions {
			if strings.Contains(command, quoteShell(binary)) {
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
	case strings.Contains(command, "setsid"):
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

func (h *fakeHost) Exists(_ context.Context, path string) (bool, error) {
	if h.shared != nil {
		return h.shared.exists(path), nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exists[path], nil
}

func (h *fakeHost) Mkdir(_ context.Context, path string) error {
	h.mu.Lock()
	h.mkdirs = append(h.mkdirs, path)
	h.mu.Unlock()
	if h.shared != nil {
		return h.shared.mkdir(path)
	}
	return nil
}

func (h *fakeHost) Chmod(_ context.Context, path string, mode fs.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chmods == nil {
		h.chmods = make(map[string]fs.FileMode)
	}
	h.chmods[path] = mode
	return nil
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
	delete(h.digests, path)
	delete(h.exists, path)
	h.mu.Unlock()
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
	return UploadSource{Path: "/local/nexterm", GOOS: "linux", GOARCH: runtime.GOARCH, Digest: digest}
}

func TestEnsureDaemonConnectsToRunningDaemon(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		pathBinary:   "/usr/bin/nexterm-server",
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		running:      true,
	}
	daemon, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != "/usr/bin/nexterm-server" {
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
	const legacy = "/usr/bin/nexterm-server"
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
		pathBinary:   "/usr/bin/nexterm-server",
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
		pathBinary:   "/usr/bin/nexterm-server",
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
	target := dataRoot + "/nexterm/bin/nexterm-" + digest[:12]
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		socketPath:   fixture.socketPath,
		digests:      map[string]string{target: digest},
		uploadDigest: digest,
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm")
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	daemon, err := resolver.EnsureDaemon(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if daemon.Binary != target {
		t.Fatalf("daemon binary = %q; want %q", daemon.Binary, target)
	}
	if len(host.uploads) != 1 {
		t.Fatalf("uploads = %v; want one staged upload", host.uploads)
	}
	staging := host.uploads[0]
	prefix := dataRoot + "/nexterm/bin/.nexterm-" + digest[:12] + "."
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
	target := dataRoot + "/nexterm/bin/nexterm-" + digest[:12]
	host := &fakeHost{
		discovery: testDiscovery(t, dataRoot),
		digests:   map[string]string{target: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	}
	host.spawn = func() {
		host.mu.Lock()
		host.running = true
		host.mu.Unlock()
	}
	resolver := testResolver(host, "/local/nexterm")
	resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
	_, err := resolver.EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("EnsureDaemon error = %v; want digest mismatch", err)
	}
}

func TestEnsureDaemonRejectsMismatchedUpload(t *testing.T) {
	other := "arm64"
	if runtime.GOARCH == "arm64" {
		other = "amd64"
	}
	host := &fakeHost{discovery: testDiscovery(t, t.TempDir())}
	resolver := testResolver(host, "/local/nexterm")
	resolver.inspect = func(string) (UploadSource, error) {
		return UploadSource{Path: "/local/nexterm", GOOS: "linux", GOARCH: other, Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, nil
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
	resolver := testResolver(host, "/local/nexterm")
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

func TestEnsureDaemonRejectsNonLinuxTarget(t *testing.T) {
	host := &fakeHost{discovery: t.TempDir() + "\nDarwin\narm64\n501\n"}
	_, err := testResolver(host, "").EnsureDaemon(context.Background(), nil)
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("EnsureDaemon error = %v; want ErrUnsupported", err)
	}
}

func TestEnsureDaemonUsesCachedUploadWithoutReuploading(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cached := dataRoot + "/nexterm/bin/nexterm-" + digest[:12]
	host := &fakeHost{
		discovery:    testDiscovery(t, dataRoot),
		probeVersion: strconv.Itoa(supervisor.ProtocolVersion),
		socketPath:   fixture.socketPath,
		running:      true,
		exists:       map[string]bool{cached: true},
		digests:      map[string]string{cached: digest},
	}
	resolver := testResolver(host, "/local/nexterm")
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
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "digest mismatch") {
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
	if !errors.Is(err, durable.ErrUnavailable) || !strings.Contains(err.Error(), "probe") {
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

func TestEnsureDaemonConcurrentLegacyUploadsShareDigestLock(t *testing.T) {
	dataRoot := t.TempDir()
	fixture := newDaemonFixture(t, dataRoot)
	shared := newSharedTarget(fixture.socketPath)
	shared.uploadStarted = make(chan struct{})
	shared.uploadRelease = make(chan struct{})
	shared.installContended = make(chan struct{})
	digest := strings.Repeat("90", 32)
	target := dataRoot + "/nexterm/bin/nexterm-" + digest[:12]

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
			resolver := testResolver(host, "/local/nexterm")
			resolver.inspect = func(string) (UploadSource, error) { return fakeUploadSource(digest), nil }
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
		t.Fatal("first legacy upload did not start")
	}
	select {
	case <-shared.installContended:
	case <-time.After(5 * time.Second):
		t.Fatal("no concurrent client contended for the legacy digest lock")
	}
	releaseUpload()
	done.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", index, err)
		}
		if daemons[index].Binary != target {
			t.Fatalf("daemon %d binary = %q; want legacy target %q", index, daemons[index].Binary, target)
		}
	}
	uploads, renames, _ := shared.counts()
	if uploads != 1 || renames != 1 {
		t.Fatalf("concurrent legacy installation saw %d uploads and %d renames; want one each", uploads, renames)
	}
}

func TestQuoteShell(t *testing.T) {
	if got := quoteShell("/tmp/a b'c"); got != `'/tmp/a b'"'"'c'` {
		t.Fatalf("quoteShell = %s", got)
	}
}
