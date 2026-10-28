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
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent/purpose"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/fs/local"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
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
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := supervisor.StateDigest(stateDir, currentUser.Uid)
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
	listenHub := purpose.NewListenHub()
	listenServer := purpose.NewListenServer(nil, listenHub)
	acceptServer := purpose.NewAcceptServer(nil, listenHub)
	rig.dial = func(ctx context.Context, purpose string) (net.Conn, error) {
		switch purpose {
		case deviceproto.PurposeTerminal:
			return dialSupervisorEndpoint(ctx, endpoint)
		case deviceproto.PurposeFile:
			clientConn, serverConn := net.Pipe()
			go func() { _ = fileServer.ServeConn(rig.serveCtx, serverConn) }()
			return clientConn, nil
		case deviceproto.PurposeForward:
			clientConn, serverConn := net.Pipe()
			go func() { _ = dialServer.ServeConn(rig.serveCtx, serverConn) }()
			return clientConn, nil
		case deviceproto.PurposeListen:
			clientConn, serverConn := net.Pipe()
			go func() { _ = listenServer.ServeConn(rig.serveCtx, serverConn) }()
			return clientConn, nil
		case deviceproto.PurposeAccept:
			clientConn, serverConn := net.Pipe()
			go func() { _ = acceptServer.ServeConn(rig.serveCtx, serverConn) }()
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

type scriptedFileSystem struct {
	roots              base.FileSystemRoots
	entries            []base.FileEntry
	listErr            error
	rootsProtocolError bool
	bridgeErr          error
}

func (s scriptedFileSystem) dial(context.Context) (net.Conn, error) {
	if s.bridgeErr != nil {
		return nil, s.bridgeErr
	}
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		kind, _, err := deviceproto.ReadFrame(serverConn)
		if err != nil {
			return
		}
		switch kind {
		case deviceproto.FrameRoots:
			if s.rootsProtocolError {
				_ = deviceproto.WriteError(serverConn, fmt.Errorf("%w: unexpected request frame %d", deviceproto.ErrProtocol, deviceproto.FrameRoots))
				return
			}
			_ = deviceproto.WriteMessage(serverConn, deviceproto.FrameRootsReply, s.roots)
		case deviceproto.FrameList:
			if s.listErr != nil {
				_ = deviceproto.WriteError(serverConn, s.listErr)
				return
			}
			_ = deviceproto.WriteMessage(serverConn, deviceproto.FrameListed, deviceproto.ListChunk{Entries: s.entries})
			_ = deviceproto.WriteFrame(serverConn, deviceproto.FrameEnd, nil)
		default:
			_ = deviceproto.WriteError(serverConn, fmt.Errorf("%w: unexpected request frame %d", deviceproto.ErrProtocol, kind))
		}
	}()
	return clientConn, nil
}

func (s scriptedFileSystem) filesystem() *fileSystem {
	return &fileSystem{dial: s.dial}
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
	platform, command := "", `printf out; printf err >&2; exit 42`
	if runtime.GOOS == "windows" {
		platform = "windows"
		command = `[Console]::Out.Write('out'); [Console]::Error.Write('err'); exit 42`
	}
	transport := New("dev-1", rig.digest, rig.dial, 1, platform)
	result, err := transport.Exec(context.Background(), command, base.ExecOptions{})
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
	if _, ok := transport.(forward.RemoteListener); !ok {
		t.Fatal("device transport must expose remote listen through listen/accept purpose bridges")
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
	command := []string{"/bin/sh"}
	input := "echo device-provider-marker\n"
	if runtime.GOOS == "windows" {
		command = []string{"powershell.exe", "-NoLogo", "-NoProfile"}
		input = "Write-Host device-provider-marker\r\n"
	}
	attachment, err := provider.Create(ctx, base.DurableCreateOptions{
		ID: tabID, Command: command, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, attachment, "device-provider-marker")
	created := attachment.(interface{ Identity() supervisor.Identity }).Identity()
	// detach keeps the device-side session; re-attach replays historical output (durable semantics).
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	reattached, err := provider.AttachWithIdentity(ctx, tabID, base.DurableIdentity{CreatedAt: created.CreatedAt, Incarnation: created.Incarnation})
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

func TestTransportFileSystemRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	filesystem, err := transport.FileSystem(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := filesystem.(base.RootsProvider)
	if !ok {
		t.Fatal("device filesystem must expose target roots through the file bridge")
	}
	roots, err := provider.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !roots.Supported {
		t.Fatal("current agent must support roots")
	}
	if runtime.GOOS == "windows" {
		if roots.PathStyle != base.PathStyleWindows {
			t.Fatalf("PathStyle = %q; want windows from the agent", roots.PathStyle)
		}
		for _, root := range roots.Roots {
			if !filepath.IsAbs(root.Path) {
				t.Fatalf("root path = %q; want an absolute agent path", root.Path)
			}
		}
	} else {
		if roots.PathStyle != base.PathStylePOSIX || len(roots.Roots) != 1 || roots.Roots[0].Path != "/" {
			t.Fatalf("roots = %+v; want the agent POSIX root", roots)
		}
	}
	wantHome, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	if roots.HomePath != wantHome {
		t.Fatalf("HomePath = %q; want agent home %q", roots.HomePath, wantHome)
	}
}

func TestFileSystemRootsRemoteWindowsAndUNCPaths(t *testing.T) {
	wantRoots := base.FileSystemRoots{
		PathStyle: base.PathStyleWindows,
		Roots: []base.FileRoot{
			{Path: `C:\`, Label: "C:"},
			{Path: `D:\`, Label: "D:"},
		},
		HomePath:  `C:\Users\agent`,
		Supported: true,
	}
	wantEntries := []base.FileEntry{
		{Name: "Project", Path: `D:\Project`, Kind: base.FileDirectory},
		{Name: "shared", Path: `\\server\share\shared`, Kind: base.FileDirectory},
	}
	filesystem := (scriptedFileSystem{roots: wantRoots, entries: wantEntries}).filesystem()
	roots, err := filesystem.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if roots.PathStyle != base.PathStyleWindows || roots.HomePath != `C:\Users\agent` || len(roots.Roots) != 2 {
		t.Fatalf("Roots = %+v; want two Windows drives and the agent home", roots)
	}
	for index := range wantRoots.Roots {
		if roots.Roots[index] != wantRoots.Roots[index] {
			t.Fatalf("root %d = %+v; want %+v", index, roots.Roots[index], wantRoots.Roots[index])
		}
	}
	entries, err := filesystem.List(context.Background(), `D:\`)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(wantEntries) {
		t.Fatalf("List = %+v; want Windows and UNC entries", entries)
	}
	for index := range wantEntries {
		if entries[index] != wantEntries[index] {
			t.Fatalf("entry %d = %+v; want unchanged absolute path %+v", index, entries[index], wantEntries[index])
		}
	}
}

func TestFileSystemRootsProtocolError(t *testing.T) {
	wantEntry := base.FileEntry{Name: "keep.txt", Path: `D:\keep.txt`, Kind: base.FileFile}
	filesystem := (scriptedFileSystem{rootsProtocolError: true, entries: []base.FileEntry{wantEntry}}).filesystem()
	roots, err := filesystem.Roots(context.Background())
	if !errors.Is(err, deviceproto.ErrProtocol) {
		t.Fatalf("Roots = %+v, %v; want deviceproto.ErrProtocol", roots, err)
	}
	if errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("protocol error was misclassified as unsupported: %v", err)
	}
	if roots.Supported || len(roots.Roots) != 0 || roots.HomePath != "" {
		t.Fatalf("Roots = %+v after a protocol error; want no fabricated roots", roots)
	}
	entries, err := filesystem.List(context.Background(), `D:\`)
	if err != nil {
		t.Fatalf("existing file operation after protocol Roots error = %v", err)
	}
	if len(entries) != 1 || entries[0] != wantEntry {
		t.Fatalf("existing List = %+v; want %+v", entries, wantEntry)
	}
}

func TestFileSystemRootsBridgeFailure(t *testing.T) {
	bridgeErr := errors.New("bridge unavailable")
	filesystem := (scriptedFileSystem{bridgeErr: bridgeErr}).filesystem()
	roots, err := filesystem.Roots(context.Background())
	if !errors.Is(err, bridgeErr) {
		t.Fatalf("Roots = %+v, %v; want the bridge error", roots, err)
	}
	if errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("bridge failure was misclassified as unsupported: %v", err)
	}
}

func TestFileSystemRootsDriveErrors(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "permission", err: fs.ErrPermission},
		{name: "no media", err: base.ErrNoMedia},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wantRoots := base.FileSystemRoots{
				PathStyle: base.PathStyleWindows,
				Roots: []base.FileRoot{
					{Path: `C:\`, Label: "C:"},
					{Path: `F:\`, Label: "F:"},
				},
				Supported: true,
			}
			filesystem := (scriptedFileSystem{
				roots:   wantRoots,
				listErr: fmt.Errorf("list F:\\: %w", testCase.err),
			}).filesystem()
			if _, err := filesystem.List(context.Background(), `F:\`); !errors.Is(err, testCase.err) {
				t.Fatalf("List error = %v; want errors.Is %v", err, testCase.err)
			}
			roots, err := filesystem.Roots(context.Background())
			if err != nil {
				t.Fatalf("Roots after one drive failed = %v", err)
			}
			if !roots.Supported || len(roots.Roots) != 2 || roots.Roots[0] != wantRoots.Roots[0] || roots.Roots[1] != wantRoots.Roots[1] {
				t.Fatalf("Roots after one drive failed = %+v; want both drives retained", roots)
			}
		})
	}
}

func TestFileSystemRootsNamespaceIsolation(t *testing.T) {
	testCases := []struct {
		name       string
		filesystem *fileSystem
		wantRoots  base.FileSystemRoots
		wantEntry  base.FileEntry
	}{
		{
			name: "owner-a-windows",
			filesystem: (scriptedFileSystem{
				roots:   base.FileSystemRoots{PathStyle: base.PathStyleWindows, Roots: []base.FileRoot{{Path: `D:\`, Label: "D:"}}, HomePath: `D:\Users\alice`, Supported: true},
				entries: []base.FileEntry{{Name: "alice.txt", Path: `D:\alice.txt`, Kind: base.FileFile}},
			}).filesystem(),
			wantRoots: base.FileSystemRoots{PathStyle: base.PathStyleWindows, Roots: []base.FileRoot{{Path: `D:\`, Label: "D:"}}, HomePath: `D:\Users\alice`, Supported: true},
			wantEntry: base.FileEntry{Name: "alice.txt", Path: `D:\alice.txt`, Kind: base.FileFile},
		},
		{
			name: "owner-b-posix",
			filesystem: (scriptedFileSystem{
				roots:   base.FileSystemRoots{PathStyle: base.PathStylePOSIX, Roots: []base.FileRoot{{Path: "/", Label: "/"}}, HomePath: "/home/bob", Supported: true},
				entries: []base.FileEntry{{Name: "bob.txt", Path: "/srv/bob/bob.txt", Kind: base.FileFile}},
			}).filesystem(),
			wantRoots: base.FileSystemRoots{PathStyle: base.PathStylePOSIX, Roots: []base.FileRoot{{Path: "/", Label: "/"}}, HomePath: "/home/bob", Supported: true},
			wantEntry: base.FileEntry{Name: "bob.txt", Path: "/srv/bob/bob.txt", Kind: base.FileFile},
		},
	}
	results := make(chan error, len(testCases))
	for _, testCase := range testCases {
		go func() {
			for iteration := 0; iteration < 25; iteration++ {
				roots, err := testCase.filesystem.Roots(context.Background())
				if err != nil {
					results <- fmt.Errorf("%s Roots: %w", testCase.name, err)
					return
				}
				if roots.PathStyle != testCase.wantRoots.PathStyle || roots.HomePath != testCase.wantRoots.HomePath || len(roots.Roots) != 1 || roots.Roots[0] != testCase.wantRoots.Roots[0] {
					results <- fmt.Errorf("%s Roots = %+v; want %+v", testCase.name, roots, testCase.wantRoots)
					return
				}
				entries, err := testCase.filesystem.List(context.Background(), testCase.wantRoots.Roots[0].Path)
				if err != nil {
					results <- fmt.Errorf("%s List: %w", testCase.name, err)
					return
				}
				if len(entries) != 1 || entries[0] != testCase.wantEntry {
					results <- fmt.Errorf("%s List = %+v; want %+v", testCase.name, entries, testCase.wantEntry)
					return
				}
			}
			results <- nil
		}()
	}
	for range testCases {
		if err := <-results; err != nil {
			t.Error(err)
		}
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
		if !filepath.IsAbs(entry.Path) || entry.Path != filepath.Join(rig.root, entry.Name) {
			t.Fatalf("entry.path = %q; want the real absolute path under %q", entry.Path, rig.root)
		}
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

func TestTransportFileSystemExtract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device extract requires a POSIX shell and tar on the agent")
	}
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	extractor, ok := filesystem.(interface {
		Extract(context.Context, string) (string, error)
	})
	if !ok {
		t.Fatal("device filesystem must expose Extract for archive extraction")
	}

	staging := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staging, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "inner", "note.txt"), []byte("extract me"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(rig.root, "bundle.tar")
	if output, err := exec.Command("tar", "-cf", archive, "-C", staging, ".").CombinedOutput(); err != nil {
		t.Fatalf("build fixture archive: %v: %s", err, output)
	}

	target, err := extractor.Extract(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if target != rig.root {
		t.Fatalf("Extract target = %q, want the archive's own directory %q", target, rig.root)
	}
	if data, err := os.ReadFile(filepath.Join(rig.root, "inner", "note.txt")); err != nil || string(data) != "extract me" {
		t.Fatalf("extracted content = %q, %v", data, err)
	}

	bogus := filepath.Join(rig.root, "bogus.tar")
	if err := os.WriteFile(bogus, []byte("not a tar archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractor.Extract(ctx, bogus); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("corrupt archive error = %v, want the tar exit status", err)
	}
	if _, err := extractor.Extract(ctx, filepath.Join(rig.root, "missing.tar")); err == nil {
		t.Fatal("missing archive must fail")
	}
	if _, err := extractor.Extract(ctx, ""); err == nil {
		t.Fatal("empty archive path must fail")
	}
}

func TestTransportFileSystemPackDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device pack download requires a POSIX shell and tar on the agent")
	}
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	filesystem, err := transport.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	packer, ok := filesystem.(interface {
		PackDownload(context.Context, string, string, sshfs.TransferOptions) (int64, error)
	})
	if !ok {
		t.Fatal("device filesystem must expose PackDownload for folder archive downloads")
	}
	deviceFilesystem, ok := filesystem.(*fileSystem)
	if !ok {
		t.Fatal("device filesystem must retain its exec bridge for archive cleanup verification")
	}
	execDevice := deviceFilesystem.exec
	var temporaryArchives []string
	deviceFilesystem.exec = func(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
		const marker = "/tmp/nexterm-pack-"
		if start := strings.Index(command, marker); start >= 0 {
			archive := command[start:]
			if end := strings.Index(archive, ".tar.gz"); end >= 0 {
				temporaryArchives = append(temporaryArchives, archive[:end+len(".tar.gz")])
			}
		}
		return execDevice(ctx, command, options)
	}
	assertNoTempArchive := func(call int) {
		t.Helper()
		if len(temporaryArchives) != call {
			t.Fatalf("tracked temp archives = %v after call %d; want one exact path per pack", temporaryArchives, call)
		}
		temporary := temporaryArchives[call-1]
		if _, err := os.Stat(temporary); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("temporary archive %q after call %d: stat error = %v; want no leftover", temporary, call, err)
		}
	}

	source := filepath.Join(rig.root, "bundle")
	if err := os.MkdirAll(filepath.Join(source, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "inner", "note.txt"), []byte("pack me"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "bundle.tar.gz")
	var progress []sshfs.Progress
	transferred, err := packer.PackDownload(ctx, source, destination, sshfs.TransferOptions{
		Progress: func(p sshfs.Progress) { progress = append(progress, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if transferred != info.Size() {
		t.Fatalf("transferred = %d, want the archive size %d", transferred, info.Size())
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done {
		t.Fatalf("progress = %+v, want a final done event", progress)
	}
	unpacked := t.TempDir()
	if output, err := exec.Command("tar", "-xzf", destination, "-C", unpacked).CombinedOutput(); err != nil {
		t.Fatalf("unpack result: %v: %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(unpacked, "bundle", "inner", "note.txt")); err != nil || string(data) != "pack me" {
		t.Fatalf("packed content = %q, %v", data, err)
	}
	assertNoTempArchive(1)

	if _, err := packer.PackDownload(ctx, filepath.Join(rig.root, "missing"), filepath.Join(t.TempDir(), "x.tar.gz"), sshfs.TransferOptions{}); err == nil || !strings.Contains(err.Error(), "create device archive") {
		t.Fatalf("packing a missing directory = %v, want the tar failure", err)
	}
	assertNoTempArchive(2)
	if _, err := packer.PackDownload(ctx, "", filepath.Join(t.TempDir(), "x.tar.gz"), sshfs.TransferOptions{}); err == nil {
		t.Fatal("empty source path must fail")
	}
}

func TestFileSystemPackDownloadCommandExpandsHome(t *testing.T) {
	var commands []string
	filesystem := &fileSystem{
		dial: func(context.Context) (net.Conn, error) { return nil, errors.New("no bridge") },
		exec: func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
			commands = append(commands, command)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		},
	}
	for _, testCase := range []struct {
		path   string
		suffix string
	}{
		{"~/packs", ` -C "$HOME" -- 'packs'`},
		{"~", ` -C "$HOME" -- '.'`},
		{"/data/dir", ` -C '/data' -- 'dir'`},
		{"/~", ` -C '/' -- '~'`},
		{"-weird", ` -C '.' -- './-weird'`},
	} {
		if _, err := filesystem.PackDownload(context.Background(), testCase.path, filepath.Join(t.TempDir(), "x.tar.gz"), sshfs.TransferOptions{}); err == nil {
			t.Fatalf("PackDownload(%q) unexpectedly succeeded without a file bridge", testCase.path)
		}
		command := commands[len(commands)-1]
		cut := strings.Index(command, " -C ")
		if !strings.HasPrefix(command, "umask 077 && tar -czf '/tmp/nexterm-pack-") || cut < 0 {
			t.Fatalf("PackDownload(%q) command = %q, want a random /tmp tar target", testCase.path, command)
		}
		if command[cut:] != testCase.suffix {
			t.Errorf("PackDownload(%q) command tail = %q, want %q", testCase.path, command[cut:], testCase.suffix)
		}
	}
}

func TestFileSystemExtractExpandsHome(t *testing.T) {
	var commands []string
	filesystem := &fileSystem{exec: func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		commands = append(commands, command)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	}}
	for _, testCase := range []struct {
		path    string
		target  string
		command string
	}{
		{"~/x.tar", "~", `tar -xf "$HOME"'/x.tar' -C "$HOME"`},
		{"~/packs/x.tar", "~/packs", `tar -xf "$HOME"'/packs/x.tar' -C "$HOME"'/packs'`},
		{"~", ".", `tar -xf "$HOME" -C '.'`},
		{"/data/x.tar", "/data", `tar -xf '/data/x.tar' -C '/data'`},
	} {
		target, err := filesystem.Extract(context.Background(), testCase.path)
		if err != nil {
			t.Fatalf("Extract(%q) = %v", testCase.path, err)
		}
		if target != testCase.target {
			t.Errorf("Extract(%q) target = %q, want %q", testCase.path, target, testCase.target)
		}
		if commands[len(commands)-1] != testCase.command {
			t.Errorf("Extract(%q) command = %q, want %q", testCase.path, commands[len(commands)-1], testCase.command)
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

func TestTransportListenRemoteEchoHalfCloseAndRelease(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	listener, err := transport.ListenRemote(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		t.Fatalf("allocated port = %q, %v; want a real device-side port", portText, err)
	}

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	origin, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = origin.Close() }()
	var forwarded net.Conn
	select {
	case forwarded = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("device inbound connection was not accepted")
	}
	if forwarded.LocalAddr().String() != address {
		t.Fatalf("forwarded local addr = %q, want the bound %q", forwarded.LocalAddr(), address)
	}
	if forwarded.RemoteAddr().String() != origin.LocalAddr().String() {
		t.Fatalf("forwarded remote addr = %q, want origin %q", forwarded.RemoteAddr(), origin.LocalAddr())
	}

	if err := origin.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Write([]byte("remote-echo")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("remote-echo"))
	if _, err := io.ReadFull(forwarded, buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := forwarded.Write(buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(origin, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "remote-echo" {
		t.Fatalf("relayed payload = %q", buffer)
	}
	closer, ok := forwarded.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("accepted conn must support CloseWrite like the SSH channel conn")
	}
	if err := closer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("origin read after half-close = %v; want io.EOF", err)
	}
	if err := forwarded.Close(); err != nil {
		t.Fatal(err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); err == nil {
		t.Fatal("Accept succeeded after Close")
	}
	// The listen-bridge close releases the device-side port asynchronously.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("device-side port still open after listener Close")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTransportListenRemoteValidationAndDialFailure(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	ctx := context.Background()
	if _, err := transport.ListenRemote(ctx, "udp", "127.0.0.1:53"); err == nil || !strings.Contains(err.Error(), "unsupported device remote listen network") {
		t.Fatalf("udp listen = %v; want the unsupported network error", err)
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	if _, err := transport.ListenRemote(ctx, "tcp", taken.Addr().String()); err == nil {
		t.Fatal("duplicate bind must surface the agent's listen failure")
	}
	failing := New("dev-1", "digest", failingDial, 1)
	if _, err := failing.ListenRemote(ctx, "tcp", "127.0.0.1:0"); err == nil {
		t.Fatal("listen must surface the bridge dial failure")
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

func TestExecCommandShellSelection(t *testing.T) {
	windows := execCommand("windows", "docker ps")
	if len(windows) != 4 || windows[0] != "powershell.exe" || windows[3] != "docker ps" {
		t.Fatalf("windows wrapper = %v, want powershell.exe -NoProfile -Command", windows)
	}
	for _, platform := range []string{"", "linux", "darwin"} {
		posix := execCommand(platform, "ls")
		if len(posix) != 3 || posix[0] != "/bin/sh" || posix[1] != "-c" || posix[2] != "ls" {
			t.Fatalf("%q wrapper = %v, want /bin/sh -c", platform, posix)
		}
	}
}
