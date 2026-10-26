//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestInspectHelperReportsTheServingProcessAndItsSessions(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	helper, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !helper.Spawned() || helper.PID() <= 0 || helper.Exited() == nil {
		t.Fatalf("spawned helper = spawned %v pid %d", helper.Spawned(), helper.PID())
	}
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID: ids.New(), Command: []string{"/bin/sh", "-c", "sleep 300"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !running.Current() || running.PID != helper.PID() || running.Activity.PTY != 1 || running.Runtime == nil {
		t.Fatalf("inspected helper = %+v, want the spawned pid %d with one PTY", running, helper.PID())
	}
}

func TestStopHelperRequiresTheInspectedHelperAndReleasesTheState(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { killHelperProcesses(t, stateDir) })
	ctx := context.Background()
	helper, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDir, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	running, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := NewRemoteProvider(helper.Client()).Create(ctx, base.DurableCreateOptions{
		ID: ids.New(), Command: []string{"/bin/sh", "-c", "sleep 300"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	// Consent covered an idle helper; it now runs a session, so it is refused.
	if err := StopHelper(ctx, stateDir, running); !errors.Is(err, ErrHelperChanged) {
		t.Fatalf("stop with stale consent = %v, want ErrHelperChanged", err)
	}
	stale := running
	stale.PID++
	if err := StopHelper(ctx, stateDir, stale); !errors.Is(err, ErrHelperChanged) {
		t.Fatalf("stop of another pid = %v, want ErrHelperChanged", err)
	}
	select {
	case <-helper.Exited():
		t.Fatal("a refused stop ended the helper")
	default:
	}
	current, err := InspectHelper(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := StopHelper(ctx, stateDir, current); err != nil {
		t.Fatal(err)
	}
	select {
	case <-helper.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("stopped helper did not exit")
	}
	unlock, err := lockHelperState(stateDir)
	if err != nil {
		t.Fatalf("state lock after stop = %v", err)
	}
	unlock()
	if _, err := InspectHelper(ctx, stateDir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("inspect after stop = %v, want ErrUnavailable", err)
	}
}

// fakeLegacyHelper serves the protocol 2 endpoint of stateDir the way a
// protocol 2 helper answered a hello offering the activity feature.
func fakeLegacyHelper(t *testing.T, stateDir string, activity ActivitySnapshot) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	endpoint, err := helperEndpointVersion(stateDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.WriteFile(filepath.Join(stateDir, "helper.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				kind, payload, err := readFrame(conn)
				if err != nil || kind != frameHello {
					return
				}
				var hello probeHelloMsg
				if unmarshalFrame(payload, &hello) != nil || hello.Version != 2 || hello.StateDigest != digest {
					reply, _ := marshalFrame(frameError, errorMsg{Code: codeVersionMismatch, Message: "unsupported"})
					_ = writeFrame(conn, frameError, reply)
					return
				}
				ack := probeHelloMsg{Version: 2, Features: []string{"activity"}, Activity: &activity}
				reply, _ := marshalFrame(frameHelloAck, ack)
				_ = writeFrame(conn, frameHelloAck, reply)
				_, _, _ = readFrame(conn)
			}()
		}
	}()
}

func TestInspectHelperCountsTheSessionsOfAProtocol2Helper(t *testing.T) {
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	fakeLegacyHelper(t, stateDir, ActivitySnapshot{PTY: 2, Exec: 1})
	running, err := InspectHelper(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if running.Current() || running.Protocol != 2 || running.Activity.Total() != 3 || running.PID != os.Getpid() {
		t.Fatalf("legacy helper = %+v, want protocol 2 with 3 sessions served by pid %d", running, os.Getpid())
	}
	// A current client meets the legacy helper as an upgrade error, never by
	// replacing it.
	if _, err := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, SpawnTimeout: 5 * time.Second}); !errors.Is(err, ErrLegacyHelperRunning) {
		t.Fatalf("connect beside a legacy helper = %v, want ErrLegacyHelperRunning", err)
	}
}

func TestInspectHelperRejectsAHelperThatIsNotThePidFileOwner(t *testing.T) {
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	fakeLegacyHelper(t, stateDir, ActivitySnapshot{PTY: 1})
	if err := os.WriteFile(filepath.Join(stateDir, "helper.pid"), []byte(strconv.Itoa(os.Getpid()+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectHelper(context.Background(), stateDir); err == nil {
		t.Fatal("inspect accepted an endpoint served by a process other than the recorded helper")
	}
}

func TestConnectHelperReportsAHelperThatExitsDuringStartup(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	started := time.Now()
	_, err := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, Executable: "/bin/false", SpawnTimeout: 30 * time.Second})
	if !errors.Is(err, ErrHelperExited) {
		t.Fatalf("connect with a crashing helper = %v, want ErrHelperExited", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("a helper that exited at once took %s to report", elapsed)
	}
}
