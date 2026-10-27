//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
func fakeLegacyHelper(t *testing.T, stateDir string, activity *ActivitySnapshot) {
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
				ack := probeHelloMsg{Version: 2, Activity: activity}
				if activity != nil {
					ack.Features = []string{"activity"}
				}
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
	fakeLegacyHelper(t, stateDir, &ActivitySnapshot{PTY: 2, Exec: 1})
	running, err := InspectHelper(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if running.Current() || running.Protocol != 2 || !running.ActivityKnown || running.Activity.Total() != 3 || running.PID != os.Getpid() {
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
	fakeLegacyHelper(t, stateDir, &ActivitySnapshot{PTY: 1})
	if err := os.WriteFile(filepath.Join(stateDir, "helper.pid"), []byte(strconv.Itoa(os.Getpid()+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InspectHelper(context.Background(), stateDir)
	if err == nil {
		t.Fatal("inspect accepted an endpoint served by a process other than the recorded helper")
	}
	if errors.Is(err, ErrHelperNotRunning) {
		t.Fatalf("a helper that answers with another pid was reported as not running: %v", err)
	}
}

func TestConnectHelperReportsAHelperThatExitsDuringStartup(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	started := time.Now()
	_, err := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, Executable: supervisorTestExecutable(t, "false"), SpawnTimeout: 30 * time.Second})
	if !errors.Is(err, ErrHelperExited) {
		t.Fatalf("connect with a crashing helper = %v, want ErrHelperExited", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("a helper that exited at once took %s to report", elapsed)
	}
}

func TestInspectHelperKeepsAnUnreportedProtocol2SessionCountUnknown(t *testing.T) {
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	fakeLegacyHelper(t, stateDir, nil)
	running, err := InspectHelper(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if running.ActivityKnown || running.Protocol != 2 {
		t.Fatalf("legacy helper without activity = %+v, want an unknown session count", running)
	}
}

func TestConnectHelperEndsAHelperThatNeverServes(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "stuck-helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Use the production startup budget so a loaded test host can start the
	// helper and publish its PID; this test asserts cleanup after that timeout.
	_, err := ConnectHelper(context.Background(), HelperConfig{StateDir: filepath.Join(dir, "state"), Executable: script})
	if err == nil || errors.Is(err, ErrHelperExited) {
		t.Fatalf("connect to a helper that never serves = %v, want a timeout", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "state", "helper.log"))
		t.Fatalf("helper PID unavailable: %v; helper log: %s", err, log)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("the helper that timed out (pid %d) is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPidTargetNeverForceKillsAnUnverifiedPid(t *testing.T) {
	child := exec.Command("/bin/sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := child.Process.Pid
	other := &pidTarget{pid: pid, reverify: func(context.Context) (int, net.Conn, error) {
		client, server := net.Pipe()
		_ = server.Close()
		return pid + 1, client, nil
	}}
	if err := other.kill(context.Background()); !errors.Is(err, ErrHelperChanged) {
		t.Fatalf("kill with another pid serving = %v, want ErrHelperChanged", err)
	}
	gone := &pidTarget{pid: pid, reverify: func(context.Context) (int, net.Conn, error) {
		return 0, nil, ErrUnavailable
	}}
	if err := gone.kill(context.Background()); err == nil {
		t.Fatal("kill without a verifiable helper succeeded")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("an unverified pid was killed: %v", err)
	}
}

func TestInspectHelperTellsNoHelperFromAnUnanswerableOne(t *testing.T) {
	stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectHelper(context.Background(), stateDir); !errors.Is(err, ErrHelperNotRunning) {
		t.Fatalf("inspect with no endpoint = %v, want ErrHelperNotRunning", err)
	}
	if _, err := ConnectHelper(context.Background(), HelperConfig{StateDir: stateDir, NoSpawn: true}); !errors.Is(err, ErrHelperNotRunning) {
		t.Fatalf("attach-only connect with no helper = %v, want ErrHelperNotRunning", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "helper.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an attach-only connect spawned a helper: %v", err)
	}
	silentHelper(t, stateDir)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = InspectHelper(ctx, stateDir)
	if err == nil || errors.Is(err, ErrHelperNotRunning) {
		t.Fatalf("inspect of a helper whose hello times out = %v, want an error other than ErrHelperNotRunning", err)
	}
}

// silentHelper listens on the current protocol endpoint of stateDir as this
// process and never answers a hello.
func silentHelper(t *testing.T, stateDir string) {
	t.Helper()
	endpoint, err := helperEndpoint(stateDir)
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
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	go func() {
		defer close(done)
		var conns []net.Conn
		defer func() {
			for _, conn := range conns {
				_ = conn.Close()
			}
		}()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns = append(conns, conn)
		}
	}()
}

func supervisorTestExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("required test executable %s: %v", name, err)
	}
	return path
}
