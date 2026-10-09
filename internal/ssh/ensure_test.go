//go:build unix

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
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

	discovery    string
	pathBinary   string
	probeVersion string
	spawn        func()
	socketPath   string
	running      bool

	shared       *sharedTarget
	uploadDigest string

	exists   map[string]bool
	digests  map[string]string
	commands []string
	mkdirs   []string
	chmods   map[string]fs.FileMode
	uploads  []string
	renames  [][2]string
}

// sharedTarget models one remote host reached by several independent SSH
// clients: the daemon socket, the uploaded files, and the spawn state are
// shared while command traces stay per client.
type sharedTarget struct {
	mu         sync.Mutex
	running    bool
	socketPath string
	files      map[string]string
	uploads    []string
	renames    [][2]string
	spawns     int
}

func newSharedTarget(socketPath string) *sharedTarget {
	return &sharedTarget{socketPath: socketPath, files: make(map[string]string)}
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
	_, ok := s.files[path]
	return ok
}

func (s *sharedTarget) upload(path, digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = digest
	s.uploads = append(s.uploads, path)
}

func (s *sharedTarget) rename(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[to] = s.files[from]
	delete(s.files, from)
	s.renames = append(s.renames, [2]string{from, to})
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
	defer h.mu.Unlock()
	h.mkdirs = append(h.mkdirs, path)
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
	h.mu.Unlock()
	if h.shared != nil {
		h.shared.upload(remotePath, h.uploadDigest)
	}
	return 1, nil
}

func (h *fakeHost) Rename(_ context.Context, from, to string) error {
	h.mu.Lock()
	h.renames = append(h.renames, [2]string{from, to})
	h.mu.Unlock()
	if h.shared != nil {
		h.shared.rename(from, to)
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
		discovery:  testDiscovery(t, dataRoot),
		socketPath: fixture.socketPath,
		digests:    map[string]string{target: digest},
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

func TestQuoteShell(t *testing.T) {
	if got := quoteShell("/tmp/a b'c"); got != `'/tmp/a b'"'"'c'` {
		t.Fatalf("quoteShell = %s", got)
	}
}
