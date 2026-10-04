//go:build unix

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestOutputContinuesAfterClientDisconnect(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `printf 'boot\n'; sleep 0.3; printf 'detached-marker\n'; sleep 0.3; printf 'late-marker\n'; sleep 30`)

	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "boot")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(800 * time.Millisecond)
	infos, err := supervisor.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Dead {
		t.Fatalf("session must keep running while detached: %+v", infos)
	}

	reattached, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	output := string(readUntil(t, reattached, "late-marker"))
	assertOrderedOnce(t, output, "boot", "detached-marker", "late-marker")

	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	assertOrderedOnce(t, string(recording), "boot", "detached-marker", "late-marker")

	if err := supervisor.Kill(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
	if err := reattached.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReattachReplayWithoutDuplicationOrRollback(t *testing.T) {
	supervisor := testSupervisor(t)
	script := `i=0; while [ "$i" -lt 20 ]; do printf 'tick:%02d\n' "$i"; i=$((i+1)); sleep 0.03; done`
	session := testScriptSession(t, supervisor, script)

	first, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, first, "tick:05")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	second, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	output := string(readToEOF(t, second))
	markers := make([]string, 0, 20)
	for index := 0; index < 20; index++ {
		markers = append(markers, fmt.Sprintf("tick:%02d", index))
	}
	assertOrderedOnce(t, output, markers...)

	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if string(recording) != output {
		t.Fatalf("replay diverges from recording:\nreplay: %q\nrecord: %q", output, recording)
	}
}

func TestReplayStaysCompleteAcrossChunkBoundaries(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `printf 'head-marker\n'; i=0; while [ "$i" -lt 40 ]; do printf 'filler-%02d-0123456789012345678901234567890123456789\n' "$i"; i=$((i+1)); done; printf 'tail-marker\n'`)

	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	output := string(readToEOF(t, attachment))
	assertOrderedOnce(t, output, "head-marker", "tail-marker")

	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if string(recording) != output {
		t.Fatal("replay must equal the recording byte for byte")
	}
}

func TestInputResizeAndExitCode(t *testing.T) {
	supervisor := testSupervisor(t)
	script := `printf 'ready\n'
while IFS= read -r line; do
  case "$line" in
    size) stty size ;;
    quit*) exit "${line#quit}" ;;
    *) printf 'in:%s\n' "$line" ;;
  esac
done`
	session := testScriptSession(t, supervisor, script)

	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	readUntil(t, attachment, "ready")

	if _, err := attachment.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "in:hello")

	if err := attachment.Resize(context.Background(), 100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Write([]byte("size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "30 100")

	if _, err := attachment.Write([]byte("quit7\n")); err != nil {
		t.Fatal(err)
	}
	err = attachment.Wait(context.Background())
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("Wait error = %v, want exit code 7", err)
	}

	info := session.Info()
	if !info.Dead || info.ExitCode == nil || *info.ExitCode != 7 {
		t.Fatalf("session info = %+v, want dead with exit code 7", info)
	}
	readToEOF(t, attachment)

	if _, err := attachment.Write([]byte("more\n")); !errors.Is(err, ErrExited) {
		t.Fatalf("Write after exit = %v, want ErrExited", err)
	}
	if err := attachment.Resize(context.Background(), 80, 24); !errors.Is(err, ErrExited) {
		t.Fatalf("Resize after exit = %v, want ErrExited", err)
	}
}

func TestWrongIdentityAttachRejected(t *testing.T) {
	supervisor := testSupervisor(t)
	id := ids.New()
	session := testScriptSessionID(t, supervisor, id, `sleep 5`)
	staleIdentity := session.Identity()

	if err := supervisor.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	replacement := testScriptSessionID(t, supervisor, id, `sleep 5`)
	if sameIdentity(staleIdentity, replacement.Identity()) {
		t.Fatal("replacement session must have a fresh identity")
	}

	if err := supervisor.kill(context.Background(), id, &staleIdentity); !errors.Is(err, ErrIdentity) {
		t.Fatalf("kill with stale identity = %v, want ErrIdentity", err)
	}
	if _, err := supervisor.Attach(context.Background(), ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach to unknown session = %v, want ErrNotFound", err)
	}
}

func TestKillRemovesArtifactsAndForgets(t *testing.T) {
	supervisor := testSupervisor(t)
	id := ids.New()
	testScriptSessionID(t, supervisor, id, `printf 'kill-me\n'; sleep 30`)
	attachment, err := supervisor.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "kill-me")

	if err := attachment.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := attachment.Wait(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait after kill detach = %v, want ErrClosed", err)
	}

	infos, err := supervisor.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == id {
			t.Fatalf("killed session still listed: %+v", info)
		}
	}
	if _, err := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session artifacts remain after kill: %v", err)
	}
	if err := supervisor.Kill(context.Background(), id); err != nil {
		t.Fatalf("second kill = %v, want nil", err)
	}
	if _, err := supervisor.Attach(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach after kill = %v, want ErrNotFound", err)
	}

	testScriptSessionID(t, supervisor, id, `printf 'reborn\n'`)
	reattached, err := supervisor.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	output := string(readToEOF(t, reattached))
	if !strings.Contains(output, "reborn") || strings.Contains(output, "kill-me") {
		t.Fatalf("recreated session replay = %q", output)
	}
}

func TestKilledSessionReportsSignal(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 30`)
	if err := supervisor.Kill(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
	err := session.Wait(context.Background())
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Signal != "SIGKILL" {
		t.Fatalf("Wait after kill = %v, want SIGKILL exit error", err)
	}
	info := session.Info()
	if info.ExitCode == nil || *info.ExitCode != -1 || info.Signal != "SIGKILL" {
		t.Fatalf("killed session info = %+v", info)
	}
}

func TestRegistryRecoveryAfterGracefulClose(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	id := ids.New()
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	testScriptSessionID(t, supervisor, id, `printf 'recover-marker\n'; sleep 30`)
	attachment, err := supervisor.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "recover-marker")
	_ = attachment.Close()
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	infos, err := restarted.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("recovered sessions = %+v, want exactly one", infos)
	}
	info := infos[0]
	if info.ID != id || !info.Dead {
		t.Fatalf("recovered info = %+v", info)
	}
	if info.ExitCode == nil || *info.ExitCode != -1 || info.Signal != "SIGKILL" {
		t.Fatalf("recovered exit status = %+v, want SIGKILL", info)
	}

	reattached, err := restarted.Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	output := string(readToEOF(t, reattached))
	if !strings.Contains(output, "recover-marker") {
		t.Fatalf("recovered replay = %q", output)
	}
	err = reattached.Wait(context.Background())
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Signal != "SIGKILL" {
		t.Fatalf("recovered Wait = %v, want SIGKILL exit error", err)
	}
	if err := restarted.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestVersionsMonotonicFloor(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 5`)
	attachment, err := session.attach()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()

	if event, grid, err := attachment.Versions(); err != nil || event != 0 || grid != 0 {
		t.Fatalf("fresh versions = %d/%d, %v", event, grid, err)
	}
	if err := attachment.PersistVersions(3, 2); err != nil {
		t.Fatal(err)
	}
	if err := attachment.PersistVersions(1, 5); err != nil {
		t.Fatal(err)
	}
	if event, grid, err := attachment.Versions(); err != nil || event != 3 || grid != 5 {
		t.Fatalf("versions = %d/%d, %v, want max floor 3/5", event, grid, err)
	}
	if err := os.WriteFile(versionsPath(supervisor.StateDir(), session.ID()), []byte("corrupt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attachment.Versions(); err == nil {
		t.Fatal("corrupt versions must fail closed")
	}
}

func TestCreateValidation(t *testing.T) {
	supervisor := testSupervisor(t)
	if _, err := supervisor.Create(context.Background(), CreateOptions{ID: "short"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid ID = %v", err)
	}
	if _, err := supervisor.Create(context.Background(), CreateOptions{Cols: 2000, Rows: 24}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid dimensions = %v", err)
	}
	if _, err := supervisor.Create(context.Background(), CreateOptions{Command: []string{"/bin/sh", "-c", "exit\x000"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NUL command = %v", err)
	}
	if _, err := supervisor.Create(context.Background(), CreateOptions{Env: []string{"NOVALUE"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad env = %v", err)
	}

	id := ids.New()
	first := testScriptSessionID(t, supervisor, id, `sleep 5`)
	if _, err := supervisor.Create(context.Background(), CreateOptions{ID: id}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create = %v", err)
	}
	if err := supervisor.Kill(context.Background(), first.ID()); err != nil {
		t.Fatal(err)
	}
	recreated := testScriptSessionID(t, supervisor, id, `sleep 5`)
	if recreated.Identity().Incarnation == first.Identity().Incarnation {
		t.Fatal("recreated session must rotate incarnation")
	}
}

func TestPrivateDirsAndFiles(t *testing.T) {
	root := t.TempDir()
	exposed := filepath.Join(root, "exposed")
	if err := os.Mkdir(exposed, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StateDir: exposed}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("world-accessible state dir = %v, want ErrInvalidInput", err)
	}

	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 5`)
	dirInfo, err := os.Lstat(sessionDir(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("session dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	for _, path := range []string{
		entryPath(supervisor.StateDir(), session.ID()),
		recordingPath(supervisor.StateDir(), session.ID()),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
}

func TestWaitContextCancel(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait with canceled context = %v", err)
	}
}
