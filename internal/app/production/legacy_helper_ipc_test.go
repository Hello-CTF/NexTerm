//go:build darwin || linux

package production

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func buildLegacyFixtureHelper(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "v2helper")
	command := exec.Command("go", "build", "-o", binary, "./internal/supervisor/testdata/v2helper")
	command.Dir = helperUpdateBuildRoot(t)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", strings.TrimSpace("GOFLAGS="+os.Getenv("GOFLAGS")+" -buildvcs=false"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build the protocol 2 fixture helper: %v\n%s", err, output)
	}
	return binary
}

func dispatchLegacyHelperOperation(t *testing.T, service *productionHelperService, sessionID, action, token string) helperOperationDTO {
	t.Helper()
	terminalService := &terminalCommandService{sessions: service.sessions, helpers: service, desktop: true}
	dispatcher := ipc.NewDispatcher()
	if err := terminalService.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(helperOperationRequest{SessionID: sessionID, Action: action, ConsentToken: token})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(t.Context(), ipc.Request{Command: "terminal_helper_operation", Args: args, ClientID: "client-a"}, ipc.Environment{ClientID: "client-a"})
	var result helperOperationDTO
	requireStoreTestResponse(t, response, &result)
	return result
}

// TestProductionHelperReplacesAProtocol2HelperOnlyWithConsent drives the
// desktop IPC against an SSH target whose helper state is still owned by a
// protocol 2 helper process with a running session.
func TestProductionHelperReplacesAProtocol2HelperOnlyWithConsent(t *testing.T) {
	helperPath, _ := buildHelperUpdateBinaries(t)
	legacyPath := buildLegacyFixtureHelper(t)
	server := newHelperUpdateExecServer(t)
	client := server.connect(t)
	resolver := &sshdaemon.Resolver{HelperExecutable: helperPath, StartTimeout: 20 * time.Second, EnsureTimeout: 120 * time.Second}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return client, nil
		}),
		DurableResolver: resolver,
		OpenAccess:      true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "legacy-host", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := &productionHelperService{ssh: resolver, sessions: manager}

	stateDir := filepath.Join(server.home, "data", "nexterm", "daemon")
	legacy := exec.Command(legacyPath, "supervisor-helper", "--state-dir", stateDir, "--session", "sleep 300")
	legacy.Env = append(os.Environ(), "HOME="+server.home, "TMPDIR="+filepath.Join(server.home, "tmp"))
	legacy.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := legacy.Start(); err != nil {
		t.Fatal(err)
	}
	legacyDone := make(chan error, 1)
	go func() { legacyDone <- legacy.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-legacy.Process.Pid, syscall.SIGKILL)
		<-legacyDone
	})
	waitForLegacyPID(t, stateDir)

	// The first ensure installs this build's helper beside the old one and
	// reports the old helper instead of starting anything; a status read
	// afterwards reports the same.
	ensured := dispatchLegacyHelperOperation(t, service, connected.ID, "update", "")
	if ensured.Legacy == nil || ensured.Legacy.PTYSessions != 1 {
		t.Fatalf("ensure beside a protocol 2 helper = %+v, want its legacy detail", ensured)
	}
	status := dispatchLegacyHelperOperation(t, service, connected.ID, "status", "")
	if status.Legacy == nil || status.Legacy.Protocol != 2 || status.Legacy.PTYSessions != 1 || !status.Legacy.ActivityKnown ||
		status.Legacy.PID != legacy.Process.Pid || status.Legacy.ConsentToken == "" || status.Compatibility != "incompatible" {
		t.Fatalf("status beside a protocol 2 helper = %+v (legacy %+v), want its pid, one session and a consent token", status, status.Legacy)
	}
	select {
	case err := <-legacyDone:
		t.Fatalf("reading the status ended the protocol 2 helper: %v", err)
	default:
	}

	replaced := dispatchLegacyHelperOperation(t, service, connected.ID, "replace_legacy", status.Legacy.ConsentToken)
	if replaced.Legacy != nil || replaced.Update == nil || replaced.Update.Status != "replaced" || replaced.Update.EndedSessions != 1 || replaced.Update.EndedCount != "exact" {
		t.Fatalf("replacement = %+v (update %+v), want the helper replaced after ending one session", replaced, replaced.Update)
	}
	select {
	case <-legacyDone:
		legacyDone <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("the protocol 2 helper is still running after the consented replacement")
	}
	if replaced.Running.Protocol == nil || *replaced.Running.Protocol != 3 {
		t.Fatalf("running helper after replacement = %+v, want protocol 3", replaced.Running)
	}
}

// legacyDeviceTransport stands in for a managed device whose agent bridges
// to the helper state of stateDir, as the agent's direct bridge does; dial,
// when set, replaces that bridge.
type legacyDeviceTransport struct {
	stateDir string
	digest   string
	dial     func(context.Context) (net.Conn, error)
}

func (d *legacyDeviceTransport) Kind() string       { return session.KindDevice }
func (d *legacyDeviceTransport) Generation() uint64 { return 1 }
func (d *legacyDeviceTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, base.ErrUnsupported
}
func (d *legacyDeviceTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (d *legacyDeviceTransport) IsAlive() bool                               { return true }
func (d *legacyDeviceTransport) Close() error                                { return nil }
func (d *legacyDeviceTransport) Provider() *supervisor.RemoteProvider {
	dial := d.dial
	if dial == nil {
		dial = func(ctx context.Context) (net.Conn, error) { return supervisor.DialHelperBridge(ctx, d.stateDir) }
	}
	return supervisor.NewRemoteProvider(supervisor.NewRemoteClient(dial, d.digest))
}

// TestProductionDeviceHelperReplacementNeedsTheServerConsent drives the same
// IPC for a managed device: the bridge reports the old helper's sessions and
// replaces it only for the consent the server sends.
func TestProductionDeviceHelperReplacementNeedsTheServerConsent(t *testing.T) {
	legacyPath := buildLegacyFixtureHelper(t)
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := exec.Command(legacyPath, "supervisor-helper", "--state-dir", stateDir, "--session", "sleep 300", "--session", "sleep 300")
	legacy.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := legacy.Start(); err != nil {
		t.Fatal(err)
	}
	legacyDone := make(chan error, 1)
	go func() { legacyDone <- legacy.Wait() }()
	t.Cleanup(func() {
		_ = syscall.Kill(-legacy.Process.Pid, syscall.SIGKILL)
		<-legacyDone
		_ = exec.Command("pkill", "-9", "-f", "supervisor-helper --state-dir "+stateDir).Run()
	})
	waitForLegacyPID(t, stateDir)
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	device := &legacyDeviceTransport{stateDir: stateDir, digest: digest}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return device, nil
		}),
		OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "legacy-device", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	service := &productionHelperService{sessions: manager}

	status := dispatchLegacyHelperOperation(t, service, connected.ID, "status", "")
	if status.Legacy == nil || status.Legacy.PTYSessions != 2 || status.Legacy.ConsentToken == "" {
		t.Fatalf("device status = %+v, want the old helper's two sessions", status.Legacy)
	}
	stale := dispatchLegacyHelperOperation(t, service, connected.ID, "replace_legacy", strings.Replace(status.Legacy.ConsentToken, ":2:", ":1:", 1))
	if stale.Legacy == nil || !stale.Legacy.Stale || stale.Legacy.ConsentToken != status.Legacy.ConsentToken {
		t.Fatalf("stale device consent = %+v, want a fresh legacy detail", stale.Legacy)
	}
	select {
	case err := <-legacyDone:
		t.Fatalf("a stale consent ended the old helper: %v", err)
	default:
	}
	replaced := dispatchLegacyHelperOperation(t, service, connected.ID, "replace_legacy", status.Legacy.ConsentToken)
	if replaced.Update == nil || replaced.Update.Status != "replaced" || replaced.Update.EndedSessions != 2 || replaced.Update.EndedCount != "exact" || replaced.Update.Forced {
		t.Fatalf("device replacement = %+v (update %+v)", replaced, replaced.Update)
	}
	select {
	case <-legacyDone:
		legacyDone <- nil
	case <-time.After(10 * time.Second):
		t.Fatal("the old device helper is still running after the consented replacement")
	}
}

func waitForLegacyPID(t *testing.T, stateDir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(stateDir, "helper.pid")); err == nil {
			if matches, _ := filepath.Glob(filepath.Join(stateDir, "sessions", "*", "session.json")); len(matches) > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("protocol 2 helper did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// failedReplacementBridge answers a replace_legacy hello the way a device
// bridge reports a replacement that ended the old helper but could not
// start the new one.
func failedReplacementBridge(context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer func() { _ = server.Close() }()
		header := make([]byte, 5)
		if _, err := io.ReadFull(server, header); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, server, int64(binary.BigEndian.Uint32(header))); err != nil {
			return
		}
		payload := []byte(`{"code":"helper_replace_failed","message":"did not start","stage":"start","stopped":true,"forced":true}`)
		frame := make([]byte, 5, 5+len(payload))
		binary.BigEndian.PutUint32(frame, uint32(len(payload)))
		frame[4] = 3 // frameError
		_, _ = server.Write(append(frame, payload...))
	}()
	return client, nil
}

func TestProductionHelperReplaceFailureKeepsItsStage(t *testing.T) {
	device := &legacyDeviceTransport{digest: "digest", dial: failedReplacementBridge}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return device, nil
		}),
		OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "failing-device", Kind: session.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	service := &productionHelperService{sessions: manager}
	terminalService := &terminalCommandService{sessions: manager, helpers: service, desktop: true}
	dispatcher := ipc.NewDispatcher()
	if err := terminalService.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(helperOperationRequest{SessionID: connected.ID, Action: "replace_legacy", ConsentToken: "2:41:1:1::"})
	if err != nil {
		t.Fatal(err)
	}
	response := dispatcher.Dispatch(t.Context(), ipc.Request{Command: "terminal_helper_operation", Args: args, ClientID: "client-a"}, ipc.Environment{ClientID: "client-a"})
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("helper_replace_failed") {
		t.Fatalf("failed replacement = %+v, want helper_replace_failed", response.Error)
	}
	detail, ok := response.Error.Detail.(*session.HelperReplaceFailure)
	if !ok || detail.Stage != "start" || !detail.Stopped || !detail.Forced || detail.RolledBack {
		t.Fatalf("failure detail = %#v, want the start stage after a forced stop", response.Error.Detail)
	}
	if !strings.Contains(response.Error.Message, "会话已终止") || !strings.Contains(response.Error.Message, "强制结束") {
		t.Fatalf("failure message = %q", response.Error.Message)
	}
}
