//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// These tests run testdata/v2helper, a separate process that speaks the
// protocol 2 wire format and keeps the on-disk contract of released 1.2.x
// helpers, and replace it through the code paths a 1.3 client uses.

func buildV2Helper(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "v2helper")
	command := exec.Command("go", "build", "-o", binary, "./testdata/v2helper")
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build the protocol 2 fixture helper: %v\n%s", err, output)
	}
	return binary
}

type v2Helper struct {
	cmd      *exec.Cmd
	stateDir string
	done     chan error
}

func startV2Helper(t *testing.T, binary, stateDir string, flags ...string) *v2Helper {
	t.Helper()
	args := append([]string{HelperCommand, "--state-dir", stateDir}, flags...)
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helper := &v2Helper{cmd: cmd, stateDir: stateDir, done: make(chan error, 1)}
	go func() { helper.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-helper.done
	})
	endpoint, err := helperEndpointVersion(stateDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if conn, err := net.Dial("unix", endpoint); err == nil {
			_ = conn.Close()
			return helper
		}
		select {
		case err := <-helper.done:
			t.Fatalf("protocol 2 helper exited during startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("protocol 2 helper did not serve")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *v2Helper) exited(t *testing.T) bool {
	t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

// v2Create creates a session the way a protocol 2 client does. An exec
// session lives as long as the returned connection.
func v2Create(t *testing.T, stateDir string, pty bool, command ...string) (string, net.Conn) {
	t.Helper()
	endpoint, err := helperEndpointVersion(stateDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := marshalFrame(frameHello, probeHelloMsg{Version: 2, StateDigest: digest, Features: []string{"exec", "metadata"}})
	if err := writeFrame(conn, frameHello, hello); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameHelloAck {
		t.Fatalf("protocol 2 hello = %d, %v", kind, err)
	}
	id := ids.New()
	create, _ := marshalFrame(frameCreate, createMsg{ID: id, Command: command, Cols: 80, Rows: 24, Pty: &pty})
	if err := writeFrame(conn, frameCreate, create); err != nil {
		t.Fatal(err)
	}
	if kind, payload, err := readFrame(conn); err != nil || kind != frameCreated {
		t.Fatalf("protocol 2 create = %d %s, %v", kind, payload, err)
	}
	return id, conn
}

func legacyStateDir(t *testing.T) string {
	t.Helper()
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	return stateDir
}

func shortenHelperStop(t *testing.T) {
	t.Helper()
	grace, kill := helperStopGrace, helperKillWait
	helperStopGrace, helperKillWait = 500*time.Millisecond, 5*time.Second
	t.Cleanup(func() { helperStopGrace, helperKillWait = grace, kill })
}

func TestProtocol2HelperUpgrade(t *testing.T) {
	binary := buildV2Helper(t)

	t.Run("a current client reports the old helper's sessions at once", func(t *testing.T) {
		stateDir := legacyStateDir(t)
		old := startV2Helper(t, binary, stateDir)
		v2Create(t, stateDir, true, "/bin/sh", "-c", "sleep 300")
		_, execConn := v2Create(t, stateDir, false, "/bin/sh", "-c", "sleep 300")
		defer func() { _ = execConn.Close() }()

		started := time.Now()
		_, err := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
		var legacy *LegacyHelperError
		if !errors.As(err, &legacy) || !errors.Is(err, ErrLegacyHelperRunning) || !errors.Is(err, ErrHelperUpgradeRequired) {
			t.Fatalf("connect beside a protocol 2 helper = %v, want a LegacyHelperError", err)
		}
		if elapsed := time.Since(started); elapsed > 10*time.Second {
			t.Fatalf("legacy helper took %s to report", elapsed)
		}
		if legacy.Helper.Protocol != 2 || legacy.Helper.PID != old.cmd.Process.Pid || !legacy.Helper.ActivityKnown ||
			legacy.Helper.Activity != (ActivitySnapshot{PTY: 1, Exec: 1}) || legacy.ConsentToken == "" {
			t.Fatalf("legacy detail = %+v, want pid %d with one PTY and one exec", legacy, old.cmd.Process.Pid)
		}

		// The same detail crosses a bridge (SSH --bridge, the device bridge).
		bridge := NewRemoteClient(func(ctx context.Context) (net.Conn, error) { return DialHelperBridge(ctx, stateDir) }, mustDigest(t, stateDir))
		_, err = bridge.List(context.Background())
		var bridged *LegacyHelperError
		if !errors.As(err, &bridged) || bridged.ConsentToken != legacy.ConsentToken || bridged.Helper.Activity != legacy.Helper.Activity {
			t.Fatalf("bridged legacy error = %v, want the same detail %+v", err, legacy)
		}
	})

	t.Run("a helper from before the activity feature reports its listed sessions", func(t *testing.T) {
		stateDir := legacyStateDir(t)
		startV2Helper(t, binary, stateDir, "--no-activity")
		v2Create(t, stateDir, true, "/bin/sh", "-c", "sleep 300")
		v2Create(t, stateDir, true, "/bin/sh", "-c", "sleep 300")
		running, err := InspectHelper(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if running.ActivityKnown || !running.PTYListed || running.Activity.PTY != 2 {
			t.Fatalf("inspected = %+v, want two listed PTY sessions with exec unknown", running)
		}
	})

	t.Run("consent replaces the helper over a bridge and keeps its recordings read-only", func(t *testing.T) {
		stateDir := legacyStateDir(t)
		old := startV2Helper(t, binary, stateDir)
		id, _ := v2Create(t, stateDir, true, "/bin/sh", "-c", "echo old-output; sleep 300")
		recording := filepath.Join(stateDir, "sessions", id, recordingName)
		waitForFileContent(t, recording, "old-output")
		bridge := NewRemoteClient(func(ctx context.Context) (net.Conn, error) { return DialHelperBridge(ctx, stateDir) }, mustDigest(t, stateDir))
		_, err := bridge.List(context.Background())
		var legacy *LegacyHelperError
		if !errors.As(err, &legacy) {
			t.Fatalf("list = %v, want a LegacyHelperError", err)
		}
		// Consent for fewer sessions than run now is refused before anything
		// is signalled, with the fresh count.
		staleConsent := legacy.Helper
		staleConsent.Activity = ActivitySnapshot{}
		err = bridge.ReplaceLegacyHelper(context.Background(), HelperConsentToken(staleConsent))
		var stale *LegacyHelperError
		if !errors.As(err, &stale) || !stale.Stale || stale.ConsentToken != legacy.ConsentToken {
			t.Fatalf("stale consent = %v, want a stale LegacyHelperError naming the current helper", err)
		}
		if err := syscall.Kill(old.cmd.Process.Pid, 0); err != nil {
			t.Fatalf("stale consent stopped the helper: %v", err)
		}

		if err := bridge.ReplaceLegacyHelper(context.Background(), legacy.ConsentToken); err != nil {
			t.Fatal(err)
		}
		if !old.exited(t) {
			t.Fatal("the protocol 2 helper is still running after the consented replacement")
		}
		running, err := InspectHelper(context.Background(), stateDir)
		if err != nil || !running.Current() {
			t.Fatalf("after replacement = %+v, %v; want this build's helper", running, err)
		}
		infos, err := bridge.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != 1 || infos[0].ID != id || !infos[0].Dead || !infos[0].Legacy {
			t.Fatalf("sessions after replacement = %+v, want the old session as a dead legacy recording", infos)
		}
		if _, err := bridge.Rename(context.Background(), RenameRequest{ID: id, Incarnation: infos[0].Incarnation, RequestID: ids.New(), Title: "x"}); !errors.Is(err, ErrExited) {
			t.Fatalf("rename of a legacy recording = %v, want ErrExited", err)
		}
		if got := readFileBytes(t, recording); !bytes.Contains(got, []byte("old-output")) {
			t.Fatalf("legacy recording = %q", got)
		}

		// An old client that starts its protocol 2 helper again cannot take
		// the state directory from this build's helper.
		again := exec.Command(binary, HelperCommand, "--state-dir", stateDir)
		output, err := again.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "locked") {
			t.Fatalf("a protocol 2 helper started beside the new one = %v %s, want a lock failure", err, output)
		}
	})

	t.Run("a helper that ignores TERM is killed and its entries stay as written", func(t *testing.T) {
		shortenHelperStop(t)
		stateDir := legacyStateDir(t)
		old := startV2Helper(t, binary, stateDir, "--ignore-term")
		id, _ := v2Create(t, stateDir, true, "/bin/sh", "-c", "sleep 300")
		entryPathname := filepath.Join(stateDir, "sessions", id, entryName)
		entryBefore := readFileBytes(t, entryPathname)
		running, err := InspectHelper(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		helper, err := ReplaceLegacyHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second}, HelperConsentToken(running))
		if err != nil {
			t.Fatal(err)
		}
		if !old.exited(t) {
			t.Fatal("the protocol 2 helper survived KILL")
		}
		infos, err := helper.Client().List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(infos) != 1 || !infos[0].Dead || !infos[0].Legacy {
			t.Fatalf("sessions = %+v, want the killed helper's session as dead legacy history", infos)
		}
		if got := readFileBytes(t, entryPathname); !bytes.Equal(got, entryBefore) {
			t.Fatalf("legacy entry was rewritten:\nbefore %s\nafter  %s", entryBefore, got)
		}
	})

	t.Run("a failed start restores the old helper and reports it", func(t *testing.T) {
		stateDir := legacyStateDir(t)
		startV2Helper(t, binary, stateDir)
		running, err := InspectHelper(context.Background(), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ReplaceLegacyHelper(context.Background(), HelperConfig{StateDir: stateDir, Executable: "/bin/false", SpawnTimeout: 10 * time.Second}, HelperConsentToken(running))
		var failed *HelperReplaceError
		if !errors.As(err, &failed) || failed.Stage != ReplaceStart || !failed.RolledBack {
			t.Fatalf("replacement with a helper that cannot start = %v, want a rolled back start failure", err)
		}
		restored, err := InspectHelper(context.Background(), stateDir)
		if err != nil || restored.Protocol != 2 || restored.PID == running.PID {
			t.Fatalf("after rollback = %+v, %v; want a new protocol 2 helper", restored, err)
		}
	})
}

func mustDigest(t *testing.T, stateDir string) string {
	t.Helper()
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitForFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q", path, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
