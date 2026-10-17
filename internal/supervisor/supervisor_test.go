//go:build unix

package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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

func TestRegistryWithoutMetadataLoadsAndPersistsRename(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	id := ids.New()
	incarnation := ids.New()
	createdAt := time.Now().UTC().Truncate(time.Nanosecond)
	if err := os.MkdirAll(sessionDir(stateDir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(`{"id":%q,"created_at":%q,"incarnation":%q,"cols":80,"rows":24,"running":false}`, id, createdAt.Format(time.RFC3339Nano), incarnation)
	if err := os.WriteFile(entryPath(stateDir, id), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	provider := NewProvider(supervisor)
	descriptors, err := requireDurableDescriptorLister(t, provider).ListDurableDescriptors(context.Background())
	if err != nil || len(descriptors) != 1 || descriptors[0].Title != "" || descriptors[0].MetadataRevision != 0 {
		t.Fatal("unexpected registry result")
	}

	request := base.DurableRenameOptions{ID: id, Incarnation: incarnation, RequestID: ids.New(), Title: "recovered-title"}
	result, err := requireDurableMetadataProvider(t, provider).RenameDurable(context.Background(), request)
	if err != nil || result.Title != request.Title || result.MetadataRevision != 1 {
		t.Fatal("unexpected registry result")
	}
	entry, err := readEntry(sessionsRoot(stateDir), id)
	if err != nil || entry.Title != request.Title || entry.MetadataRevision != 1 {
		t.Fatal("unexpected registry result")
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	provider = NewProvider(restarted)
	descriptors, err = requireDurableDescriptorLister(t, provider).ListDurableDescriptors(context.Background())
	if err != nil || len(descriptors) != 1 || descriptors[0].Title != request.Title || descriptors[0].MetadataRevision != 1 {
		t.Fatal("unexpected registry result")
	}
	retry, err := requireDurableMetadataProvider(t, provider).RenameDurable(context.Background(), request)
	if err != nil || retry != result {
		t.Fatal("unexpected registry result")
	}
}

func TestCanonicalMetadataConsistentAcrossInfoSurfaces(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	server := testServer(t, supervisor)
	client := testClient(t, server)
	id := ids.New()
	session, err := supervisor.Create(context.Background(), CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	incarnation := session.Identity().Incarnation
	const title = "consistent-title"
	const revision = uint64(1)
	if _, err := supervisor.Rename(context.Background(), RenameRequest{
		ID: id, Incarnation: incarnation, RequestID: ids.New(), Title: title,
	}); err != nil {
		t.Fatal(err)
	}

	assertConsistent := func(currentSession *Session, currentSupervisor *Supervisor, currentClient *Client) {
		t.Helper()
		checkInfo := func(surface string, info Info) {
			t.Helper()
			if info.ID != id || info.Incarnation != incarnation || info.Title != title || info.MetadataRevision != revision {
				t.Fatalf("%s = %+v, want title %q revision %d", surface, info, title, revision)
			}
		}
		checkInfo("Session.Info", currentSession.Info())
		supervisorInfos, err := currentSupervisor.List(context.Background())
		if err != nil || len(supervisorInfos) != 1 {
			t.Fatalf("Supervisor.List = %+v, %v", supervisorInfos, err)
		}
		checkInfo("Supervisor.List", supervisorInfos[0])
		wireInfos, err := currentClient.List(context.Background())
		if err != nil || len(wireInfos) != 1 {
			t.Fatalf("wire List = %+v, %v", wireInfos, err)
		}
		checkInfo("wire List", wireInfos[0])
		descriptors, err := NewRemoteProvider(currentClient).ListDurableDescriptors(context.Background())
		if err != nil || len(descriptors) != 1 {
			t.Fatalf("durable descriptors = %+v, %v", descriptors, err)
		}
		descriptor := descriptors[0]
		if descriptor.ID != id || descriptor.Incarnation != incarnation || descriptor.Title != title || descriptor.MetadataRevision != revision {
			t.Fatalf("durable descriptor = %+v, want title %q revision %d", descriptor, title, revision)
		}
	}
	assertConsistent(session, supervisor, client)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedServer := testServer(t, restarted)
	restartedClient := testClient(t, restartedServer)
	recovered, err := restarted.resolveSession(id)
	if err != nil {
		t.Fatal(err)
	}
	assertConsistent(recovered, restarted, restartedClient)
}

func TestRenameConcurrentAttachmentIDAndVersions(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 30`)
	attachment, err := session.attach(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	id := session.ID()
	incarnation := session.Identity().Incarnation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	var ready sync.WaitGroup
	var readers sync.WaitGroup
	ready.Add(2)
	readers.Add(2)
	errs := make(chan error, 2)
	for _, checkVersions := range []bool{false, true} {
		go func() {
			defer readers.Done()
			ready.Done()
			<-start
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if attachment.ID() != id {
					errs <- fmt.Errorf("attachment ID changed from %q to %q", id, attachment.ID())
					return
				}
				if checkVersions {
					if _, _, err := attachment.Versions(); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	var renameErr error
	for revision := uint64(0); revision < 200; revision++ {
		_, err := supervisor.Rename(context.Background(), RenameRequest{
			ID:                       id,
			Incarnation:              incarnation,
			RequestID:                ids.New(),
			ExpectedMetadataRevision: revision,
			Title:                    fmt.Sprintf("title-%d", revision),
		})
		if err != nil {
			renameErr = err
			break
		}
	}
	cancel()
	readers.Wait()
	if renameErr != nil {
		t.Fatal(renameErr)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestVersionsMonotonicFloor(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `sleep 5`)
	attachment, err := session.attach(0)
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

func TestSessionEnvInjectsTerminalDefaults(t *testing.T) {
	unsetTerminalEnv(t)

	environment := sessionEnv([]string{"PATH=/usr/bin"})
	if got := environmentValue(environment, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := environmentValue(environment, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", got)
	}
	if got := environmentValue(environment, "LANG"); got != "C.UTF-8" {
		t.Fatalf("LANG = %q, want C.UTF-8", got)
	}
	if got := environmentValue(environment, "PATH"); got != "/usr/bin" {
		t.Fatalf("PATH override lost: %q", got)
	}

	withLocale := sessionEnv([]string{"LC_ALL=C"})
	if got := environmentValue(withLocale, "LANG"); got != "" {
		t.Fatalf("LANG = %q despite LC_ALL, want unset", got)
	}
}

func TestSessionEnvRespectsExplicitValues(t *testing.T) {
	unsetTerminalEnv(t)

	environment := sessionEnv([]string{"TERM=vt100", "COLORTERM=ansi", "LANG=zh_CN.UTF-8"})
	if got := environmentValue(environment, "TERM"); got != "vt100" {
		t.Fatalf("TERM = %q, want vt100", got)
	}
	if got := environmentValue(environment, "COLORTERM"); got != "ansi" {
		t.Fatalf("COLORTERM = %q, want ansi", got)
	}
	if got := environmentValue(environment, "LANG"); got != "zh_CN.UTF-8" {
		t.Fatalf("LANG = %q, want zh_CN.UTF-8", got)
	}

	emptied := sessionEnv([]string{"TERM="})
	if got := environmentValue(emptied, "TERM"); got != "xterm-256color" {
		t.Fatalf("empty TERM = %q, want fallback xterm-256color", got)
	}
	occurrences := 0
	for _, entry := range emptied {
		if strings.HasPrefix(entry, "TERM=") {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("TERM entries = %d, want exactly one", occurrences)
	}
}

func TestDefaultShellCommandFallbackIsNotALoginShell(t *testing.T) {
	if value, ok := os.LookupEnv("SHELL"); ok {
		t.Cleanup(func() { _ = os.Setenv("SHELL", value) })
	}
	if err := os.Unsetenv("SHELL"); err != nil {
		t.Fatal(err)
	}
	if got := defaultShellCommand(nil); !slices.Equal(got, []string{"/bin/sh"}) {
		t.Fatalf("defaultShellCommand without SHELL = %v, want [/bin/sh]", got)
	}
	if err := os.Setenv("SHELL", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if got := defaultShellCommand(nil); !slices.Equal(got, []string{"/bin/sh", "-l"}) {
		t.Fatalf("defaultShellCommand with explicit SHELL=/bin/sh = %v, want [/bin/sh -l]", got)
	}
}

func TestDefaultShellCommandEnvironmentShellKeepsLoginFlag(t *testing.T) {
	if got := defaultShellCommand([]string{"SHELL=/bin/zsh"}); !slices.Equal(got, []string{"/bin/zsh", "-l"}) {
		t.Fatalf("defaultShellCommand with environment SHELL = %v, want [/bin/zsh -l]", got)
	}
}

func TestCreateDefaultCommandSpawnsFallbackShell(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	if value, ok := os.LookupEnv("SHELL"); ok {
		t.Cleanup(func() { _ = os.Setenv("SHELL", value) })
	}
	if err := os.Unsetenv("SHELL"); err != nil {
		t.Fatal(err)
	}
	supervisor := testSupervisor(t)
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Env:  []string{"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.entry.Command; !slices.Equal(got, []string{"/bin/sh"}) {
		t.Fatalf("registry command = %v; want [/bin/sh] without -l", got)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	if _, err := session.Write([]byte("printf 'fallback-ok\\n'\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, attachment, "fallback-ok")
	killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.killAndWait(killCtx, supervisor.commandTimeout); err != nil {
		t.Fatal(err)
	}
}

func TestCreateInjectsTermWhenEnvironmentLacksIt(t *testing.T) {
	unsetTerminalEnv(t)
	supervisor := testSupervisor(t)
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{"/bin/sh", "-c", `printf 'term=%s\n' "${TERM:-unset}"`},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	output := string(readUntil(t, attachment, "\r\n"))
	if !strings.Contains(output, "term=xterm-256color") {
		t.Fatalf("session env output = %q, want injected TERM", output)
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

// TestAttachFromOffset proves a resumed attachment seeks into the recording:
// the stream starts at the requested absolute offset, NextSeq tracks it, and
// an offset beyond the recording size is rejected instead of silently
// clamping.
func TestAttachFromOffset(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, `printf 'one two three\n'`)
	probe, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	full := readToEOF(t, probe)
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	offset := uint64(strings.Index(string(full), "two"))
	if offset == 0 {
		t.Fatalf("recording %q does not contain the marker", full)
	}

	resumed, err := supervisor.AttachFrom(context.Background(), session.ID(), offset)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	if resumed.NextSeq() != offset {
		t.Fatalf("resumed NextSeq = %d, want %d", resumed.NextSeq(), offset)
	}
	if got, want := readToEOF(t, resumed), []byte("two three\r\n"); !bytes.Equal(got, want) {
		t.Fatalf("resumed stream = %q, want %q", got, want)
	}

	if _, err := supervisor.AttachFrom(context.Background(), session.ID(), uint64(len(full))+1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("attach beyond recording = %v, want ErrInvalidInput", err)
	}
}
