//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/pty"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestRemoteProviderCreateLostResponseNeverKillsReplacement(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	provider := NewRemoteProvider(client)

	realCreate := remoteProviderCreate
	t.Cleanup(func() { remoteProviderCreate = realCreate })
	remoteProviderCreate = func(createCtx context.Context, createClient *Client, options CreateOptions) (Info, error) {
		if _, err := createClient.Create(createCtx, options); err != nil {
			return Info{}, err
		}
		if err := supervisor.Kill(context.Background(), options.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := createClient.Create(createCtx, CreateOptions{
			ID:      options.ID,
			Attempt: ids.New(),
			Command: []string{"/bin/sh", "-c", `printf 'replacement\n'; sleep 30`},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		}); err != nil {
			t.Fatal(err)
		}
		return Info{}, errors.New("create response lost")
	}

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
	if len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("replacement incarnation must survive attempt reconciliation: %+v", infos)
	}
	stream, err := client.Attach(context.Background(), id, nil)
	if err != nil {
		t.Fatalf("replacement must stay attachable: %v", err)
	}
	defer func() { _ = stream.Close() }()
	readUntil(t, stream, "replacement")
	if err := supervisor.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestKillIdentityProofRefusesUnprovenLeftovers(t *testing.T) {
	supervisor := testSupervisor(t)
	ctx := context.Background()
	root := sessionsRoot(supervisor.StateDir())

	id := ids.New()
	dir := sessionDir(supervisor.StateDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := registryEntry{
		ID:          id,
		CreatedAt:   time.Now(),
		Incarnation: ids.New(),
		Cols:        80,
		Rows:        24,
	}
	if err := writeEntry(root, entry); err != nil {
		t.Fatal(err)
	}
	foreign := Identity{CreatedAt: entry.CreatedAt.Add(time.Hour), Incarnation: ids.New()}
	if err := supervisor.kill(ctx, id, &foreign); !errors.Is(err, ErrIdentity) {
		t.Fatalf("kill with foreign identity = %v, want ErrIdentity", err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("unproven leftover must not be cleaned: %v", err)
	}
	matching := Identity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}
	if err := supervisor.kill(ctx, id, &matching); err != nil {
		t.Fatalf("kill with proven identity = %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proven leftover must be cleaned: %v", err)
	}

	entryless := ids.New()
	entrylessDir := sessionDir(supervisor.StateDir(), entryless)
	if err := os.MkdirAll(entrylessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.kill(ctx, entryless, &matching); !errors.Is(err, ErrIdentity) {
		t.Fatalf("kill without entry proof = %v, want ErrIdentity", err)
	}
	if _, err := os.Lstat(entrylessDir); err != nil {
		t.Fatalf("entryless leftover must not be cleaned by identity kill: %v", err)
	}
	if err := supervisor.kill(ctx, entryless, nil); err != nil {
		t.Fatalf("operator kill of entryless leftover = %v", err)
	}
	if _, err := os.Lstat(entrylessDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operator kill must clean entryless leftover: %v", err)
	}
}

func TestInFlightReplacementCreateSurvivesIdentityKill(t *testing.T) {
	supervisor := testSupervisor(t)
	ctx := context.Background()
	id := ids.New()

	original := testScriptSessionID(t, supervisor, id, `sleep 30`)
	originalIdentity := original.Identity()
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}

	realStart := ptyStart
	started := make(chan struct{})
	release := make(chan struct{})
	ptyStart = func(startCtx context.Context, config pty.Config) (*pty.Session, error) {
		close(started)
		<-release
		return realStart(startCtx, config)
	}
	t.Cleanup(func() { ptyStart = realStart })

	createDone := make(chan error, 1)
	go func() {
		_, err := supervisor.Create(ctx, CreateOptions{
			ID:      id,
			Command: []string{"/bin/sh", "-c", `printf 'replacement\n'; sleep 30`},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		createDone <- err
	}()
	<-started

	killDone := make(chan error, 1)
	go func() { killDone <- supervisor.kill(ctx, id, &originalIdentity) }()
	time.Sleep(150 * time.Millisecond)
	close(release)

	if err := <-createDone; err != nil {
		t.Fatalf("replacement Create = %v", err)
	}
	if err := <-killDone; !errors.Is(err, ErrIdentity) {
		t.Fatalf("stale identity kill = %v, want ErrIdentity", err)
	}

	infos, err := supervisor.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || infos[0].Incarnation == originalIdentity.Incarnation {
		t.Fatalf("replacement session must be registered intact: %+v", infos)
	}
	if _, err := os.Lstat(entryPath(supervisor.StateDir(), id)); err != nil {
		t.Fatalf("replacement registry entry must survive: %v", err)
	}
	if _, err := os.Lstat(recordingPath(supervisor.StateDir(), id)); err != nil {
		t.Fatalf("replacement recording must survive: %v", err)
	}
	attachment, err := supervisor.Attach(ctx, id)
	if err != nil {
		t.Fatalf("replacement must be attachable: %v", err)
	}
	defer func() { _ = attachment.Close() }()
	readUntil(t, attachment, "replacement")

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileCreateCleansLeftoverAttemptArtifacts(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	attempt := ids.New()
	dir := sessionDir(supervisor.StateDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeEntry(sessionsRoot(supervisor.StateDir()), registryEntry{
		ID:        id,
		CreatedAt: time.Now(),
		Attempt:   attempt,
		Cols:      80,
		Rows:      24,
	}); err != nil {
		t.Fatal(err)
	}

	if err := client.ReconcileCreate(ctx, id, ids.New()); !errors.Is(err, ErrIdentity) {
		t.Fatalf("reconcile with foreign attempt = %v, want ErrIdentity", err)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("foreign attempt must not clean leftovers: %v", err)
	}
	if err := client.ReconcileCreate(ctx, id, attempt); err != nil {
		t.Fatalf("reconcile with owning attempt = %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owning attempt must clean leftovers: %v", err)
	}
	if err := client.ReconcileCreate(ctx, ids.New(), attempt); err != nil {
		t.Fatalf("reconcile of unknown session = %v, want nil", err)
	}
}
