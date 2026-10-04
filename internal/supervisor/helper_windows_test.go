//go:build windows

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"golang.org/x/sys/windows"
)

const windowsReadTimeout = 30 * time.Second

func readPipeUntil(t *testing.T, reader io.Reader, marker string) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	found := make(chan result, 1)
	go func() {
		var buffer strings.Builder
		chunk := make([]byte, 4096)
		for {
			count, err := reader.Read(chunk)
			if count > 0 {
				buffer.Write(chunk[:count])
				if strings.Contains(buffer.String(), marker) {
					found <- result{data: []byte(buffer.String())}
					return
				}
			}
			if err != nil {
				found <- result{data: []byte(buffer.String()), err: err}
				return
			}
		}
	}()
	select {
	case result := <-found:
		if result.err != nil {
			t.Fatalf("read until %q: %v (data so far %q)", marker, result.err, result.data)
		}
		return result.data
	case <-time.After(windowsReadTimeout):
		t.Fatalf("timed out waiting for %q", marker)
		return nil
	}
}

const (
	windowsSpawnChildFileEnv = "NEXTERM_SUPERVISOR_WINDOWS_SPAWN_FILE"
	windowsHelperStateDirEnv = "NEXTERM_SUPERVISOR_WINDOWS_HELPER_DIR"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperCommand {
		os.Exit(RunHelperCLI(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestPipeListenDialRoundtrip(t *testing.T) {
	name, err := pipeEndpointName(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenSocket(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	type result struct {
		kind frameType
		data []byte
		err  error
	}
	server := make(chan result, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()
		kind, payload, err := readFrame(conn)
		if err != nil {
			server <- result{err: err}
			return
		}
		if err := writeFrame(conn, frameHelloAck, []byte(`{"version":2}`)); err != nil {
			server <- result{err: err}
			return
		}
		server <- result{kind: kind, data: payload}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := dialSocket(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := writeFrame(conn, frameHello, []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	kind, _, err := readFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	if kind != frameHelloAck {
		t.Fatalf("response frame = %d, want hello ack", kind)
	}
	received := <-server
	if received.err != nil {
		t.Fatal(received.err)
	}
	if received.kind != frameHello {
		t.Fatalf("server frame = %d, want hello", received.kind)
	}
}

func TestPipeListenerCloseUnblocksAccept(t *testing.T) {
	name, err := pipeEndpointName(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenSocket(name)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-accepted:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Accept stayed blocked after Close")
	}
}

func TestPipeCurrentUserSecurity(t *testing.T) {
	sddl, err := currentUserPipeSDDL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sddl, "(A;;GA;;;SY)") {
		t.Fatalf("pipe SDDL %q does not grant the system account", sddl)
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sddl, user.User.Sid.String()) {
		t.Fatalf("pipe SDDL %q does not grant the current user %s", sddl, user.User.Sid.String())
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	if !securityDescriptor.IsValid() {
		t.Fatal("pipe security descriptor is invalid")
	}
}

func TestHelperEndpointPipeIsVersionedAndScoped(t *testing.T) {
	first, err := helperEndpoint(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := helperEndpoint(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, pipePrefix) {
		t.Fatalf("endpoint %q is not a named pipe", first)
	}
	if !strings.Contains(first, fmt.Sprintf("nexterm-supervisor-v%d-", ProtocolVersion)) {
		t.Fatalf("endpoint %q is not versioned", first)
	}
	sid, err := currentUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, sid) {
		t.Fatalf("endpoint %q does not include the current user SID %s", first, sid)
	}
	if first == second {
		t.Fatalf("endpoint %q is not scoped to its state directory", first)
	}
	if _, err := HelperEndpoint(""); err == nil {
		t.Fatal("empty state directory accepted")
	}
}

func TestLockHelperStateExclusiveWindows(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockHelperState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockHelperState(stateDir); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second lock = %v, want ErrAlreadyExists", err)
	}
	unlock()
	second, err := lockHelperState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestConnectHelperProtocolMismatchPipeFailsWithoutSpawn(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := pipeEndpointName(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	var connections atomic.Int32
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			kind, _, err := readFrame(conn)
			if err != nil || kind != frameHello {
				_ = conn.Close()
				continue
			}
			_ = writeFrame(conn, frameError, []byte(`{"code":"version_mismatch","message":"protocol version 99 is not supported"}`))
			_ = conn.Close()
		}
	}()
	_, err = ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 5 * time.Second})
	if err == nil {
		t.Fatal("connect to a wrong-version pipe helper succeeded")
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("connect error = %v, want a protocol error", err)
	}
	time.Sleep(300 * time.Millisecond)
	if count := connections.Load(); count != 1 {
		t.Fatalf("fake pipe helper saw %d connections, want exactly one probe (no spawn)", count)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "helper.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protocol mismatch spawned a helper (log present): %v", err)
	}
}

func TestWindowsHelperSpawnReattachAndKill(t *testing.T) {
	stateDir := os.Getenv(windowsHelperStateDirEnv)
	if stateDir == "" {
		stateDir = filepath.Join(t.TempDir(), "state")
		t.Cleanup(func() { killHelperProcessesWindows(t, stateDir) })
	}
	ctx := context.Background()
	first, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Spawned() {
		t.Fatal("first connect did not spawn the helper")
	}
	id := ids.New()
	attachment, err := NewRemoteProvider(first.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NoExit", "-Command", "Write-Host win-helper-marker"},
		Env:     []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, attachment, "win-helper-marker")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if second.Spawned() {
		t.Fatal("second connect spawned a new helper")
	}
	infos, err := second.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || infos[0].Dead {
		t.Fatalf("sessions after client close = %+v", infos)
	}
	reattached, err := NewRemoteProvider(second.Client()).Attach(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	readPipeUntil(t, reattached, "win-helper-marker")
	if _, err := reattached.Write([]byte("Write-Host win-helper-input\r\n")); err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, reattached, "win-helper-input")
	if err := reattached.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := reattached.Kill(ctx); err != nil {
		rawErr := second.Client().Kill(ctx, id, nil)
		t.Fatalf("kill: %v (raw client: %v)", err, rawErr)
	}
	infos, err = second.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("session survived kill: %+v", infos)
	}
}

func TestSpawnDetachedWindows(t *testing.T) {
	markerFile := filepath.Join(t.TempDir(), "spawned")
	t.Setenv(windowsSpawnChildFileEnv, markerFile)
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	if err := spawnDetached(os.Args[0], []string{"-test.run=^TestSpawnDetachedChildWindows$"}, logPath); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(markerFile); err == nil {
			return
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("spawned child never wrote its marker: %s", data)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSpawnDetachedChildWindows(t *testing.T) {
	path := os.Getenv(windowsSpawnChildFileEnv)
	if path == "" {
		t.Skip("spawn detached child")
	}
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func killHelperProcessesWindows(t *testing.T, stateDir string) {
	t.Helper()
	script := fmt.Sprintf("Get-CimInstance Win32_Process -Filter \"CommandLine LIKE '%%%s --state-dir %s%%'\" | Where-Object { $_.ProcessId -ne $PID } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }", HelperCommand, stateDir)
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-Command", script)
	_ = cmd.Run()
	waitScript := fmt.Sprintf("Get-CimInstance Win32_Process -Filter \"CommandLine LIKE '%%%s --state-dir %s%%'\" | Where-Object { $_.ProcessId -ne $PID } | Measure-Object | Select-Object -ExpandProperty Count", HelperCommand, stateDir)
	deadline := time.Now().Add(15 * time.Second)
	for {
		output, err := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-Command", waitScript).Output()
		if err == nil && strings.TrimSpace(string(output)) == "0" {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestEnsurePrivateDirWindows(t *testing.T) {
	normal := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(normal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(normal); err != nil {
		t.Fatalf("normal Windows directory rejected: %v", err)
	}
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(file); err == nil {
		t.Fatal("plain file accepted as a private directory")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(t.TempDir(), "junction")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junction, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v (%s)", err, output)
	}
	if err := ensurePrivateDir(junction); err == nil {
		t.Fatal("junction accepted as a private directory")
	}
}

func TestOpenRecordingWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.raw")
	if err := os.WriteFile(path, []byte("recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openRecording(path)
	if err != nil {
		t.Fatalf("normal Windows recording rejected: %v", err)
	}
	_ = file.Close()
}

func TestListenPipeLifecycle(t *testing.T) {
	name, err := pipeEndpointName(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe startup: %v", err)
	}
	if _, err := listenPipe(name); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second listenPipe = %v, want ErrAlreadyExists", err)
	}
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := dialSocket(ctx, name)
	if err != nil {
		t.Fatalf("dial with pending Accept: %v", err)
	}
	result := <-accepted
	if result.err != nil {
		t.Fatalf("Accept: %v", result.err)
	}
	serverConn := result.conn
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(serverConn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("pipe read = %q, %v", buffer, err)
	}
	if _, err := serverConn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "pong" {
		t.Fatalf("duplex pipe read = %q, %v", buffer, err)
	}
	_ = conn.Close()
	_ = serverConn.Close()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe restart after Close: %v", err)
	}
	_ = restarted.Close()
}

type aceInfo struct {
	trustee string
	mask    uint32
	flags   uint8
}

func privateDACLTrustees(t *testing.T, path string) []aceInfo {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	base := unsafe.Pointer(dacl)
	offset := 8
	aces := make([]aceInfo, 0, dacl.AceCount)
	for index := 0; index < int(dacl.AceCount); index++ {
		header := (*windows.ACE_HEADER)(unsafe.Add(base, offset))
		ace := (*windows.ACCESS_ALLOWED_ACE)(unsafe.Add(base, offset))
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		aces = append(aces, aceInfo{trustee: trustee, mask: uint32(ace.Mask), flags: header.AceFlags})
		offset += int(header.AceSize)
	}
	return aces
}

func currentPrincipalSIDs(t *testing.T) map[string]bool {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sids := map[string]bool{user.User.Sid.String(): true}
	groups, err := token.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		sids[group.Sid.String()] = true
	}
	return sids
}

func requirePrivateDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("directory DACL is not protected against inheritance")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if !currentPrincipalSIDs(t)[owner.String()] {
		t.Fatalf("directory owner = %s, want the current user or one of its groups", owner.String())
	}
	for _, ace := range privateDACLTrustees(t, path) {
		if ace.trustee != "S-1-5-18" && ace.trustee != currentUserSIDForTest(t) {
			t.Fatalf("directory DACL grants access to foreign trustee %s", ace.trustee)
		}
		if ace.mask&(windows.GENERIC_ALL|0x001F01FF) == 0 {
			t.Fatalf("directory DACL trustee %s lacks full-control rights", ace.trustee)
		}
	}
}

func currentUserSIDForTest(t *testing.T) string {
	t.Helper()
	sid, err := currentUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func requireInheritedPrivateDACL(t *testing.T, path string) {
	t.Helper()
	sid, err := currentUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	aces := privateDACLTrustees(t, path)
	if len(aces) == 0 {
		t.Fatal("child object inherited no ACEs")
	}
	for _, ace := range aces {
		if ace.trustee != "S-1-5-18" && ace.trustee != sid {
			t.Fatalf("child object DACL grants access to foreign trustee %s", ace.trustee)
		}
		if ace.mask&(windows.GENERIC_ALL|0x001F01FF) == 0 {
			t.Fatalf("child object DACL trustee %s lacks full-control rights", ace.trustee)
		}
		if ace.flags&windows.INHERITED_ACE == 0 {
			t.Fatalf("child object ACE for %s is not marked inherited", ace.trustee)
		}
	}
}

func TestEnsurePrivateDirSetsProtectedDACL(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	requirePrivateDACL(t, stateDir)
	child := filepath.Join(stateDir, "sessions", "abc")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	requireInheritedPrivateDACL(t, child)
	file := filepath.Join(stateDir, "session.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireInheritedPrivateDACL(t, file)
}

func TestEnsurePrivateDirPermissiveParentWindows(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	permissive, err := windows.SecurityDescriptorFromString("D:(A;OICI;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := permissive.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(parent, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	requirePrivateDACL(t, stateDir)
}

func TestPipeFullDuplexConcurrentIO(t *testing.T) {
	name, err := pipeEndpointName(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := dialSocket(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := <-accepted
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buffer := make([]byte, 4096)
		for {
			count, err := serverConn.Read(buffer)
			if count > 0 {
				if _, writeErr := serverConn.Write(buffer[:count]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	const rounds = 50
	var expected strings.Builder
	for index := 0; index < rounds; index++ {
		fmt.Fprintf(&expected, "ping-%02d", index)
	}
	received := make(chan []byte, rounds*2)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buffer := make([]byte, 4096)
		for {
			count, err := client.Read(buffer)
			if count > 0 {
				received <- append([]byte(nil), buffer[:count]...)
			}
			if err != nil {
				return
			}
		}
	}()
	for index := 0; index < rounds; index++ {
		if _, err := client.Write([]byte(fmt.Sprintf("ping-%02d", index))); err != nil {
			t.Fatal(err)
		}
	}
	var echoed strings.Builder
	for echoed.Len() < expected.Len() {
		select {
		case chunk := <-received:
			echoed.Write(chunk)
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d of %d full-duplex echo bytes arrived", echoed.Len(), expected.Len())
		}
	}
	if echoed.String() != expected.String() {
		t.Fatalf("echo stream = %q, want %q", echoed.String(), expected.String())
	}
	closed := make(chan struct{})
	go func() {
		_ = client.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("client Close hung with a pending read")
	}
	<-readerDone
	_ = serverConn.Close()
	<-serverDone
}

func fakePipeStreamServer(t *testing.T, stateDir string, handle func(conn net.Conn, kind frameType, payload []byte) bool) string {
	t.Helper()
	name, err := pipeEndpointName(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := listenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		kind, payload, err := readFrame(conn)
		if err != nil || kind != frameHello {
			return
		}
		var hello helloMsg
		if err := unmarshalFrame(payload, &hello); err != nil {
			return
		}
		if hello.Version != ProtocolVersion || hello.StateDigest != digest {
			_ = writeFrame(conn, frameError, []byte(`{"code":"state_mismatch","message":"wrong state"}`))
			return
		}
		if err := writeFrame(conn, frameHelloAck, []byte(`{"version":2}`)); err != nil {
			return
		}
		kind, payload, err = readFrame(conn)
		if err != nil || kind != frameAttach {
			return
		}
		attached, err := marshalFrame(frameAttached, Info{ID: ids.New(), CreatedAt: time.Now(), Incarnation: ids.New(), Cols: 80, Rows: 24})
		if err != nil {
			return
		}
		if err := writeFrame(conn, frameAttached, attached); err != nil {
			return
		}
		for {
			kind, payload, err = readFrame(conn)
			if err != nil {
				return
			}
			if !handle(conn, kind, payload) {
				return
			}
		}
	}()
	return name
}

func TestPipeStreamCloseReturnsWhenServerStalls(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	name := fakePipeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		return true
	})
	client := NewClient(name, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("stalled"))
		writeErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	original := streamCloseDrainTimeout
	streamCloseDrainTimeout = 500 * time.Millisecond
	defer func() { streamCloseDrainTimeout = original }()
	closed := make(chan struct{})
	go func() {
		_ = stream.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close hung on a stalling pipe server")
	}
	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("Write succeeded against a stalling pipe server")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("in-flight Write never woke after Close")
	}
}

func TestPipeStreamCloseNoInflight(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	name := fakePipeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind == frameDetach {
			return false
		}
		return true
	})
	client := NewClient(name, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		_ = stream.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close without in-flight requests hung on a pipe stream")
	}
}

const (
	windowsHelperAppDirEnv   = "NEXTERM_SUPERVISOR_WINDOWS_APP_DIR"
	windowsHelperAppReadyEnv = "NEXTERM_SUPERVISOR_WINDOWS_APP_READY"
	windowsSecondUserName    = "nxsuphelper"
	windowsSecondUserPass    = "NxSup!2026test"
)

func TestWindowsHelperSessionsSurviveAppProcessExitAndReattach(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcessesWindows(t, stateDir) })
	readyFile := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsHelperAppProcess$", "-test.v")
	cmd.Env = append(os.Environ(), windowsHelperAppDirEnv+"="+stateDir, windowsHelperAppReadyEnv+"="+readyFile)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("app process never became ready: %s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("app process exit: %v\n%s", err, output.String())
	}
	sessionID, err := os.ReadFile(readyFile)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	restarted, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Spawned() {
		t.Fatal("app restart spawned a second helper instead of reattaching")
	}
	infos, err := restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != string(sessionID) || infos[0].Dead {
		t.Fatalf("sessions after app exit = %+v", infos)
	}
	attachment, err := NewRemoteProvider(restarted.Client()).Attach(ctx, string(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	replayed := readPipeUntil(t, attachment, "win-app-input")
	if count := strings.Count(string(replayed), "win-app-boot"); count != 1 {
		t.Fatalf("boot marker replayed %d times: %q", count, replayed)
	}
	if _, err := attachment.Write([]byte("Write-Host win-app-post\r\n")); err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, attachment, "win-app-post")
	if err := attachment.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	infos, err = restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Cols != 100 || infos[0].Rows != 30 {
		t.Fatalf("resized session grid = %+v, want 100x30", infos)
	}
	if err := attachment.Kill(ctx); err != nil {
		rawErr := restarted.Client().Kill(ctx, string(sessionID), nil)
		t.Fatalf("kill: %v (raw client: %v)", err, rawErr)
	}
	infos, err = restarted.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("session survived kill: %+v", infos)
	}
}

func TestWindowsHelperAppProcess(t *testing.T) {
	stateDir := os.Getenv(windowsHelperAppDirEnv)
	if stateDir == "" {
		t.Skip("windows helper app process")
	}
	ctx := context.Background()
	helper, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID:      ids.New(),
		Command: []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NoExit", "-Command", "Write-Host win-app-boot"},
		Env:     []string{"TERM=xterm-256color"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, attachment, "win-app-boot")
	if _, err := attachment.Write([]byte("Write-Host win-app-input\r\n")); err != nil {
		t.Fatal(err)
	}
	readPipeUntil(t, attachment, "win-app-input")
	if err := os.WriteFile(os.Getenv(windowsHelperAppReadyEnv), []byte(attachment.ID()), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestStateDirDeniesSecondUser(t *testing.T) {
	if err := exec.Command("net", "session").Run(); err != nil {
		t.Skip("elevation is required for the second-user denial test")
	}
	_ = exec.Command("net", "user", windowsSecondUserName, "/delete").Run()
	if output, err := exec.Command("net", "user", windowsSecondUserName, windowsSecondUserPass, "/add").CombinedOutput(); err != nil {
		t.Skipf("cannot create the second test user: %v (%s)", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command("net", "user", windowsSecondUserName, "/delete").Run()
	})
	base := `C:\Windows\Temp`
	stateDir := filepath.Join(base, "nxsup-state-"+ids.New())
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	if err := ensurePrivateDir(stateDir); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(stateDir, "session.json")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(base, "nxsup-control-"+ids.New()+".txt")
	if err := os.WriteFile(shared, []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(shared) })
	sid, err := currentUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	grantDACL(t, shared, "D:(A;;GA;;;SY)(A;;GA;;;WD)(A;;GA;;;"+sid+")")

	withSecondUser(t, func() {
		if _, err := os.ReadFile(shared); err != nil {
			t.Fatalf("control read as the second user: %v", err)
		}
		if _, err := os.ReadFile(secret); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("second user read the private state file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "hack.txt"), []byte("x"), 0o600); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("second user wrote into the private state directory: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(stateDir, "hack.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second-user write artifact present: %v", err)
	}
}

var (
	advapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW              = advapi32.NewProc("LogonUserW")
	procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")
)

func withSecondUser(t *testing.T, fn func()) {
	t.Helper()
	username, err := windows.UTF16PtrFromString(windowsSecondUserName)
	if err != nil {
		t.Fatal(err)
	}
	domain, err := windows.UTF16PtrFromString(".")
	if err != nil {
		t.Fatal(err)
	}
	password, err := windows.UTF16PtrFromString(windowsSecondUserPass)
	if err != nil {
		t.Fatal(err)
	}
	var token windows.Handle
	result, _, err := procLogonUserW.Call(
		uintptr(unsafe.Pointer(username)),
		uintptr(unsafe.Pointer(domain)),
		uintptr(unsafe.Pointer(password)),
		uintptr(3),
		uintptr(0),
		uintptr(unsafe.Pointer(&token)),
	)
	if result == 0 {
		t.Fatalf("LogonUser: %v", err)
	}
	defer func() { _ = windows.CloseHandle(token) }()
	if result, _, err := procImpersonateLoggedOnUser.Call(uintptr(token)); result == 0 {
		t.Fatalf("ImpersonateLoggedOnUser: %v", err)
	}
	defer func() { _ = windows.RevertToSelf() }()
	fn()
}

func grantDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
