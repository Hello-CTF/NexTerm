//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func seedVictimDir(t *testing.T, supervisor *Supervisor) string {
	t.Helper()
	victim := filepath.Join(supervisor.StateDir(), "victim")
	if err := os.MkdirAll(filepath.Join(victim, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep", "data"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	return victim
}

func assertVictimIntact(t *testing.T, victim string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(victim, "keep", "data"))
	if err != nil || string(data) != "precious" {
		t.Fatalf("victim directory was tampered with: %q, %v", data, err)
	}
}

func TestKillRejectsInvalidID(t *testing.T) {
	supervisor := testSupervisor(t)
	victim := seedVictimDir(t, supervisor)
	ctx := context.Background()

	for _, id := range []string{"../victim", "victim/../victim", "/etc/passwd", "short", "aaaaaaaaaaaaaaaaaaaaaaaaaa2"} {
		if err := supervisor.Kill(ctx, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Kill(%q) = %v, want ErrInvalidInput", id, err)
		}
	}
	assertVictimIntact(t, victim)

	if err := removeRegistryArtifacts(sessionsRoot(supervisor.StateDir()), "../victim"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("removeRegistryArtifacts traversal = %v, want ErrInvalidInput", err)
	}
	assertVictimIntact(t, victim)

	if _, err := supervisor.Attach(ctx, "../victim"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Attach traversal = %v, want ErrInvalidInput", err)
	}
	assertVictimIntact(t, victim)
}

func TestIPCKillRejectsInvalidID(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()
	victim := seedVictimDir(t, supervisor)

	if err := client.Kill(ctx, "../victim", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Client.Kill traversal = %v, want ErrInvalidInput", err)
	}
	assertVictimIntact(t, victim)

	if _, err := client.Attach(ctx, "../victim", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Client.Attach traversal = %v, want ErrInvalidInput", err)
	}
	assertVictimIntact(t, victim)
}

func TestRemoteProviderCreateDoesNotCompensateDefiniteFailures(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	provider := NewRemoteProvider(client)
	ctx := context.Background()

	id := ids.New()
	existing, err := provider.Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `printf 'ready\n'; while IFS= read -r line; do printf 'in:%s\n' "$line"; done`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = existing.Close() }()
	readUntil(t, existing, "ready")

	if _, err := provider.Create(ctx, base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
	}); !errors.Is(err, durable.ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v, want durable.ErrAlreadyExists", err)
	}

	infos, err := supervisor.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("duplicate Create must not harm the existing session: %+v", infos)
	}
	if _, err := existing.Write([]byte("ping\n")); err != nil {
		t.Fatalf("existing session must stay writable: %v", err)
	}
	readUntil(t, existing, "in:ping")

	if _, err := provider.Create(ctx, base.DurableCreateOptions{ID: "bad-id"}); !errors.Is(err, durable.ErrInvalidInput) {
		t.Fatalf("invalid Create = %v, want durable.ErrInvalidInput", err)
	}
	infos, err = supervisor.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("invalid Create must not harm sessions: %+v", infos)
	}
}

func TestRemoteProviderCreateCompensatesOnlyOwnedSessions(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	provider := NewRemoteProvider(client)

	realCreate := remoteProviderCreate
	t.Cleanup(func() { remoteProviderCreate = realCreate })

	lostResponse := func(createCtx context.Context, createClient *Client, options CreateOptions) (Info, error) {
		if _, err := createClient.Create(createCtx, options); err != nil {
			return Info{}, err
		}
		return Info{}, errors.New("create response lost")
	}

	t.Run("generated ID lost response is cleaned up", func(t *testing.T) {
		remoteProviderCreate = lostResponse
		_, err := provider.Create(context.Background(), base.DurableCreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		if err == nil {
			t.Fatal("Create must surface the lost response")
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(infos) != 0 {
			t.Fatalf("owned session must be compensated away: %+v", infos)
		}
	})

	t.Run("explicit ID lost response is rolled back by attempt", func(t *testing.T) {
		remoteProviderCreate = lostResponse
		id := ids.New()
		_, err := provider.Create(context.Background(), base.DurableCreateOptions{
			ID:      id,
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		if err == nil {
			t.Fatal("Create must surface the lost response")
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(infos) != 0 {
			t.Fatalf("create attempt ownership must allow rollback of the explicit-ID session: %+v", infos)
		}
	})

	t.Run("attempt mismatch never kills an existing session", func(t *testing.T) {
		remoteProviderCreate = realCreate
		id := ids.New()
		if _, err := client.Create(context.Background(), CreateOptions{
			ID:      id,
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := client.ReconcileCreate(context.Background(), id, ids.New()); !errors.Is(err, ErrIdentity) {
			t.Fatalf("reconcile with foreign attempt = %v, want ErrIdentity", err)
		}
		infos, listErr := supervisor.List(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(infos) != 1 {
			t.Fatalf("foreign attempt must not kill the session: %+v", infos)
		}
		if err := supervisor.Kill(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRemoteProviderCreateRejectsReplacedIncarnation(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	provider := NewRemoteProvider(client)

	realAttach := remoteProviderAttach
	t.Cleanup(func() { remoteProviderAttach = realAttach })
	remoteProviderAttach = func(attachCtx context.Context, attachClient *Client, id string, expect *Identity) (*Stream, error) {
		if err := supervisor.Kill(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if _, err := attachClient.Create(attachCtx, CreateOptions{
			ID:      id,
			Command: []string{"/bin/sh", "-c", `printf 'replacement\n'; sleep 30`},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		}); err != nil {
			t.Fatal(err)
		}
		return realAttach(attachCtx, attachClient, id, expect)
	}

	id := ids.New()
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if attachment != nil {
		_ = attachment.Close()
	}
	if !errors.Is(err, durable.ErrIdentity) {
		t.Fatalf("Create across replaced incarnation = %v, want durable.ErrIdentity", err)
	}

	infos, listErr := supervisor.List(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("replacement session must be preserved: %+v", infos)
	}
	stream, err := client.Attach(context.Background(), id, nil)
	if err != nil {
		t.Fatalf("replacement session must stay attachable: %v", err)
	}
	defer func() { _ = stream.Close() }()
	output := string(readUntil(t, stream, "replacement"))
	if !strings.Contains(output, "replacement") {
		t.Fatalf("replacement replay = %q", output)
	}
	if err := supervisor.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCShutdownCanceledContextThenRestart(t *testing.T) {
	supervisor := testSupervisor(t)
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	server, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatal(err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Shutdown(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown with canceled context = %v, want context.Canceled", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("second Shutdown = %v, want completion result", err)
	}

	restarted, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("restart after canceled Shutdown = %v, want lock released", err)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := restarted.Shutdown(finishCtx); err != nil {
		t.Fatal(err)
	}
}

func TestIPCConcurrentShutdown(t *testing.T) {
	supervisor := testSupervisor(t)
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	server, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			if slot%2 == 0 {
				canceledCtx, cancel := context.WithCancel(context.Background())
				cancel()
				errs[slot] = server.Shutdown(canceledCtx)
				return
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			errs[slot] = server.Shutdown(shutdownCtx)
		}(index)
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("concurrent Shutdown %d = %v", slot, err)
		}
	}

	restarted, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("restart after concurrent Shutdown = %v, want lock released", err)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := restarted.Shutdown(finishCtx); err != nil {
		t.Fatal(err)
	}
}
