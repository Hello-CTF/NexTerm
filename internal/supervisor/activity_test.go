//go:build unix

package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestActivitySnapshotIsDaemonWideAcrossClients(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	clientA := testClient(t, server)
	clientB := testClient(t, server)
	ctx := context.Background()
	info, err := clientA.Create(ctx, CreateOptions{
		Command: []string{"/bin/sh"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	streamA, err := clientA.Attach(ctx, info.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = streamA.Close() }()
	streamB, err := clientB.Attach(ctx, info.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = streamB.Close() }()

	execCtx, cancelExec := context.WithCancel(context.Background())
	execDone := make(chan error, 1)
	execFinished := make(chan struct{})
	go func() {
		_, _, _, _, err := clientB.Exec(execCtx, ExecSpec{Command: []string{"sleep", "30"}})
		execDone <- err
		close(execFinished)
	}()
	t.Cleanup(func() {
		cancelExec()
		select {
		case <-execFinished:
		case <-time.After(10 * time.Second):
			t.Error("exec did not stop during cleanup")
		}
	})
	awaitSessionCount(t, supervisor, 2)

	snapshot, known, err := clientA.Activity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("daemon-wide activity is unknown")
	}
	if snapshot.PTY != 1 || snapshot.Exec != 1 || snapshot.Total() != 2 || !snapshot.Active() {
		t.Fatalf("activity = %+v, total %d, active %v; want one PTY and one exec", snapshot, snapshot.Total(), snapshot.Active())
	}
	if err := snapshot.RequireIdle(); !errors.Is(err, ErrActiveSessions) {
		t.Fatalf("snapshot idle guard = %v, want ErrActiveSessions", err)
	}
	if err := clientA.RequireIdle(ctx); !errors.Is(err, ErrActiveSessions) {
		t.Fatalf("client idle guard = %v, want ErrActiveSessions", err)
	}
	infos, err := clientA.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != info.ID {
		t.Fatalf("durable List = %+v; activity must not rely on this PTY-only view", infos)
	}

	if _, err := streamA.Write([]byte("printf 'activity-alive\\n'\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, streamB, "activity-alive")

	cancelExec()
	select {
	case err := <-execDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exec cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not stop after cancellation")
	}
	awaitSessionCount(t, supervisor, 1)
	snapshot, known, err = clientB.Activity(ctx)
	if err != nil || !known || snapshot.PTY != 1 || snapshot.Exec != 0 {
		t.Fatalf("activity after exec = %+v, known %v, %v; want only the shared PTY", snapshot, known, err)
	}

	if err := clientA.Kill(ctx, info.ID, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, known, err = clientB.Activity(ctx)
	if err != nil || !known || snapshot.Active() {
		t.Fatalf("activity after kill = %+v, known %v, %v; want idle", snapshot, known, err)
	}
	if err := snapshot.RequireIdle(); err != nil {
		t.Fatalf("idle snapshot guard = %v", err)
	}
	if err := clientB.RequireIdle(ctx); err != nil {
		t.Fatalf("idle client guard = %v", err)
	}
}

func TestActivitySnapshotExcludesDeadAndRecoveredSessions(t *testing.T) {
	supervisor := testSupervisor(t)
	session := testScriptSession(t, supervisor, "exit 0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := supervisor.Activity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active() {
		t.Fatalf("exited session is active: %+v", snapshot)
	}
	infos, err := supervisor.List(ctx)
	if err != nil || len(infos) != 1 || !infos[0].Dead {
		t.Fatalf("exited durable session = %+v, %v; want retained dead state", infos, err)
	}

	recoveredID := ids.New()
	recovered := newRecoveredSession(supervisor, registryEntry{
		ID:          recoveredID,
		CreatedAt:   time.Now(),
		Incarnation: ids.New(),
		Running:     true,
	})
	supervisor.mu.Lock()
	supervisor.sessions[recoveredID] = recovered
	supervisor.mu.Unlock()
	snapshot, err = supervisor.Activity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active() {
		t.Fatalf("dead-recovered session is active: %+v", snapshot)
	}
}

func TestActivityUnknownFromOlderDaemon(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	client := NewClient(fakeHelloAckServer(t, stateDir, helloMsg{Version: ProtocolVersion}), stateDir)
	snapshot, known, err := client.Activity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if known || snapshot != (ActivitySnapshot{}) {
		t.Fatalf("activity = %+v, known = %v; want explicit unknown", snapshot, known)
	}
	if err := client.RequireIdle(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("idle guard with unknown activity = %v, want ErrUnsupported", err)
	}
}
