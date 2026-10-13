package device

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent/purpose"
	"github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// purposeRig serves all three bridge purposes in memory: terminal through a
// real supervisor helper endpoint, file and forward through the agent-side
// purpose servers over net.Pipe. The servers run under a rig-scoped context
// mirroring the agent runtime's runCtx: a client's operation ctx never
// reaches the server side across the bridge.
type purposeRig struct {
	digest   string
	endpoint string
	root     string
	echo     string
	serveCtx context.Context
	dial     DialFunc
}

func newPurposeRig(t *testing.T) *purposeRig {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.NewServer(instance, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	serveCtx, stopServe := context.WithCancel(context.Background())
	t.Cleanup(stopServe)
	rig := &purposeRig{
		digest:   digest,
		endpoint: endpoint,
		root:     t.TempDir(),
		echo:     startEchoListener(t),
		serveCtx: serveCtx,
	}
	fileServer := purpose.NewFileServer(nil)
	dialServer := purpose.NewDialServer(nil)
	rig.dial = func(ctx context.Context, purpose string) (net.Conn, error) {
		switch purpose {
		case deviceproto.PurposeTerminal:
			return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
		case deviceproto.PurposeFile:
			clientConn, serverConn := net.Pipe()
			go func() { _ = fileServer.ServeConn(rig.serveCtx, serverConn) }()
			return clientConn, nil
		case deviceproto.PurposeForward:
			clientConn, serverConn := net.Pipe()
			go func() { _ = dialServer.ServeConn(rig.serveCtx, serverConn) }()
			return clientConn, nil
		default:
			return nil, fmt.Errorf("unexpected bridge purpose %q", purpose)
		}
	}
	return rig
}

func failingDial(context.Context, string) (net.Conn, error) {
	return nil, errors.New("bridge unavailable")
}

func TestTransportKindGenerationAndLifecycle(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 7)
	if transport.Kind() != "device" {
		t.Fatalf("Kind() = %q; want device", transport.Kind())
	}
	if transport.Generation() != 7 {
		t.Fatalf("Generation() = %d; want 7", transport.Generation())
	}
	if transport.DeviceID() != "dev-1" {
		t.Fatalf("DeviceID() = %q; want dev-1", transport.DeviceID())
	}
	if !transport.IsAlive() {
		t.Fatal("IsAlive() = false before Close; want true")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.IsAlive() {
		t.Fatal("IsAlive() = true after Close; want false")
	}
}

func TestTransportExecSplitStreamsAndExitCode(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	result, err := transport.Exec(context.Background(), `printf out; printf err >&2; exit 42`, base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "out" || result.Stderr != "err" {
		t.Fatalf("stdout=%q stderr=%q, want split streams", result.Stdout, result.Stderr)
	}
	if result.ExitCode == nil || *result.ExitCode != 42 {
		t.Fatalf("exitCode=%v, want 42", result.ExitCode)
	}
}

func TestTransportCapabilitySurface(t *testing.T) {
	rig := newPurposeRig(t)
	var transport base.Transport = New("dev-1", rig.digest, rig.dial, 1)
	if _, ok := transport.(base.PTYTransport); ok {
		t.Fatal("device transport must not expose PTY channels; tabs are supervisor durable sessions")
	}
	if _, ok := transport.(base.FileTransport); !ok {
		t.Fatal("device transport must expose a filesystem through file-purpose bridges")
	}
	if _, ok := transport.(base.Dialer); !ok {
		t.Fatal("device transport must expose a dialer through forward-purpose bridges")
	}
}

func TestTransportPingRoundTrip(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	if _, err := transport.Ping(context.Background()); err != nil {
		t.Fatalf("Ping = %v; want a supervisor list round-trip", err)
	}
}

func TestTransportPingDialFailure(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, failingDial, 1)
	if _, err := transport.Ping(context.Background()); err == nil {
		t.Fatal("Ping must fail when the bridge dial fails")
	}
}

func TestTransportProviderCreateAttachRoundTrip(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	provider := transport.Provider()
	ctx := context.Background()
	tabID := ids.New()
	attachment, err := provider.Create(ctx, base.DurableCreateOptions{
		ID: tabID, Command: []string{"/bin/sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("echo device-provider-marker\n")); err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, attachment, "device-provider-marker")
	// detach keeps the device-side session; re-attach replays historical output (durable semantics).
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	reattached, err := provider.Attach(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, reattached, "device-provider-marker")
	if err := reattached.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reattached.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportFileSystemUnary(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := filesystem.(interface {
		ReadRange(context.Context, string, int64, int64) ([]byte, int64, error)
	}); !ok {
		t.Fatal("device filesystem must expose ReadRange for the file editor probe")
	}

	file := filepath.Join(rig.root, "note.txt")
	if err := filesystem.WriteFile(ctx, file, []byte("hello device"), false); err != nil {
		t.Fatal(err)
	}
	data, err := filesystem.ReadFile(ctx, file, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello device" {
		t.Fatalf("ReadFile = %q; want hello device", data)
	}
	size, err := filesystem.Size(ctx, file)
	if err != nil || size != int64(len("hello device")) {
		t.Fatalf("Size = %d, %v; want %d", size, err, len("hello device"))
	}
	exists, err := filesystem.Exists(ctx, file)
	if err != nil || !exists {
		t.Fatalf("Exists = %v, %v; want true", exists, err)
	}
	exists, err = filesystem.Exists(ctx, filepath.Join(rig.root, "missing"))
	if err != nil || exists {
		t.Fatalf("Exists missing = %v, %v; want false, nil", exists, err)
	}
	if _, err := filesystem.ReadFile(ctx, filepath.Join(rig.root, "missing"), 1<<20); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile missing = %v; want fs.ErrNotExist across the bridge", err)
	}

	// Backup writes keep the previous content at the .nexterm-bak sibling.
	if err := filesystem.WriteFile(ctx, file, []byte("version two"), true); err != nil {
		t.Fatal(err)
	}
	backup, err := filesystem.ReadFile(ctx, file+".nexterm-bak", 1<<20)
	if err != nil || string(backup) != "hello device" {
		t.Fatalf("backup = %q, %v; want hello device", backup, err)
	}

	sub := filepath.Join(rig.root, "sub")
	if err := filesystem.Mkdir(ctx, sub); err != nil {
		t.Fatal(err)
	}
	entries, err := filesystem.List(ctx, rig.root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	kinds := map[string]base.FileKind{}
	for _, entry := range entries {
		names = append(names, entry.Name)
		kinds[entry.Name] = entry.Kind
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"note.txt", "note.txt.nexterm-bak", "sub"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("List = %q; want %q present", joined, want)
		}
	}
	if kinds["sub"] != base.FileDirectory || kinds["note.txt"] != base.FileFile {
		t.Fatalf("List kinds = %v; want sub dir and note.txt file", kinds)
	}

	renamed := filepath.Join(rig.root, "renamed.txt")
	if err := filesystem.Rename(ctx, file, renamed); err != nil {
		t.Fatal(err)
	}
	if exists, _ := filesystem.Exists(ctx, file); exists {
		t.Fatal("old path still exists after rename")
	}

	content, err := filesystem.ReadFile(ctx, renamed, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	md5sum := md5.Sum(content)
	if sum, err := filesystem.Checksum(ctx, renamed, "md5"); err != nil || sum != hex.EncodeToString(md5sum[:]) {
		t.Fatalf("Checksum md5 = %q, %v; want %q", sum, err, hex.EncodeToString(md5sum[:]))
	}
	sha256sum := sha256.Sum256(content)
	if sum, err := filesystem.Checksum(ctx, renamed, "sha256"); err != nil || sum != hex.EncodeToString(sha256sum[:]) {
		t.Fatalf("Checksum sha256 = %q, %v; want %q", sum, err, hex.EncodeToString(sha256sum[:]))
	}
	if _, err := filesystem.Checksum(ctx, renamed, "sha512"); err == nil {
		t.Fatal("unsupported checksum algorithm must fail")
	}

	if runtime.GOOS != "windows" {
		if err := filesystem.Chmod(ctx, renamed, 0o640); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(renamed)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %o; want 640", info.Mode().Perm())
		}
	}

	if err := filesystem.Delete(ctx, renamed, false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, sub, true); err != nil {
		t.Fatal(err)
	}
	if exists, _ := filesystem.Exists(ctx, renamed); exists {
		t.Fatal("renamed.txt still exists after delete")
	}
}

func TestTransportFileSystemStreamsAndRange(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(rig.root, "stream.bin")
	content := make([]byte, 70*1024)
	for i := range content {
		content[i] = byte(i % 251)
	}

	writer, err := filesystem.OpenWrite(ctx, file, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	appender, err := filesystem.OpenWrite(ctx, file, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appender.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := filesystem.OpenRead(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Size() != int64(len(content)+4) {
		t.Fatalf("OpenRead size = %d; want %d", reader.Size(), len(content)+4)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if len(data) != len(content)+4 || string(data[len(data)-4:]) != "tail" {
		t.Fatalf("streamed content = %d bytes ending %q; want append round-trip", len(data), data[len(data)-4:])
	}

	ranger, ok := filesystem.(interface {
		ReadRange(context.Context, string, int64, int64) ([]byte, int64, error)
	})
	if !ok {
		t.Fatal("filesystem lost the ReadRange surface")
	}
	window, total, err := ranger.ReadRange(ctx, file, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(content)+4) || len(window) != 20 || window[0] != content[10] {
		t.Fatalf("ReadRange = %d bytes of %d; want the byte window", len(window), total)
	}
	window, total, err = ranger.ReadRange(ctx, file, int64(len(content)+100), 20)
	if err != nil || len(window) != 0 {
		t.Fatalf("ReadRange past EOF = %d bytes, %v; want empty", len(window), err)
	}
}

func TestTransportFileSystemTransferHelpers(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.bin")
	content := make([]byte, 150*1024)
	for i := range content {
		content[i] = byte(i % 239)
	}
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	remotePath := filepath.Join(rig.root, "uploaded.bin")
	transferred, err := local.Upload(ctx, source, filesystem, remotePath, local.TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transferred != int64(len(content)) {
		t.Fatalf("Upload transferred %d; want %d", transferred, len(content))
	}
	destination := filepath.Join(t.TempDir(), "downloaded.bin")
	transferred, err = local.Download(ctx, filesystem, remotePath, destination, local.TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if transferred != int64(len(content)) {
		t.Fatalf("Download transferred %d; want %d", transferred, len(content))
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloaded) != len(content) {
		t.Fatalf("downloaded %d bytes; want %d", len(downloaded), len(content))
	}
	for i := range content {
		if downloaded[i] != content[i] {
			t.Fatalf("byte %d = %d; want %d", i, downloaded[i], content[i])
		}
	}
}

func TestTransportFileSystemDialFailure(t *testing.T) {
	transport := New("dev-1", "digest", failingDial, 1)
	filesystem, err := transport.FileSystem(context.Background())
	if err != nil {
		t.Fatalf("FileSystem must not dial eagerly: %v", err)
	}
	if _, err := filesystem.List(context.Background(), "/"); err == nil {
		t.Fatal("List must surface the bridge dial failure at operation time")
	}
}

func TestTransportDialContextEcho(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	conn, err := transport.DialContext(ctx, "tcp", rig.echo)
	if err != nil {
		t.Fatal(err)
	}
	if conn.LocalAddr().String() != "dev-1" || conn.RemoteAddr().String() != rig.echo {
		t.Fatalf("addrs = %q -> %q; want dev-1 -> %q", conn.LocalAddr(), conn.RemoteAddr(), rig.echo)
	}
	message := []byte("forward through the device")
	if _, err := conn.Write(message); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != string(message) {
		t.Fatalf("echo = %q; want %q", echoed, message)
	}
	closer, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("device dial conn must support CloseWrite like the SSH stream conn")
	}
	if err := closer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after half-close = %v; want io.EOF", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportDialContextSurvivesDialCancel(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	dialCtx, cancel := context.WithCancel(context.Background())
	conn, err := transport.DialContext(dialCtx, "tcp", rig.echo)
	if err != nil {
		t.Fatal(err)
	}
	// The forward service cancels the dial ctx right after DialContext
	// returns; the proxied connection must stay usable, mirroring SSH
	// channel semantics.
	cancel()
	message := []byte("still alive")
	if _, err := conn.Write(message); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(message))
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != string(message) {
		t.Fatalf("echo after dial cancel = %q; want %q", echoed, message)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportDialContextFailures(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	if _, err := transport.DialContext(ctx, "udp", "127.0.0.1:53"); err == nil || !strings.Contains(err.Error(), "unsupported device dial network") {
		t.Fatalf("udp dial = %v; want the unsupported network error", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	if _, err := transport.DialContext(ctx, "tcp", address); err == nil {
		t.Fatal("dial to a closed port must fail")
	}
	failing := New("dev-1", "digest", failingDial, 1)
	if _, err := failing.DialContext(ctx, "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dial must surface the bridge dial failure")
	}
}

func startEchoListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go echoConn(conn)
		}
	}()
	return listener.Addr().String()
}

// echoConn mirrors bytes and propagates the half-close so the other side
// observes EOF after its CloseWrite.
func echoConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	buffer := make([]byte, 64*1024)
	for {
		count, err := conn.Read(buffer)
		if count > 0 {
			if _, writeErr := conn.Write(buffer[:count]); writeErr != nil {
				return
			}
		}
		if err != nil {
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			return
		}
	}
}

func readUntilMarker(t *testing.T, attachment base.DurableAttachment, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	buffer := make([]byte, 32*1024)
	var received []byte
	for time.Now().Before(deadline) {
		count, err := attachment.Read(buffer)
		received = append(received, buffer[:count]...)
		if strings.Contains(string(received), marker) {
			return
		}
		if err != nil {
			t.Fatalf("read = %v after %q; want marker %q", err, received, marker)
		}
	}
	t.Fatalf("timed out waiting for %q; received %q", marker, received)
}
