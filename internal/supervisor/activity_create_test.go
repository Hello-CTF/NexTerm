//go:build unix

package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/pty"
)

func TestActivityIncludesCreateInFlight(t *testing.T) {
	supervisor := testSupervisor(t)
	realStart := supervisor.ptyStart
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	supervisor.ptyStart = func(ctx context.Context, config pty.Config) (*pty.Session, error) {
		close(started)
		<-release
		return realStart(ctx, config)
	}
	type outcome struct {
		session *Session
		err     error
	}
	created := make(chan outcome, 1)
	go func() {
		session, err := supervisor.Create(context.Background(), CreateOptions{
			Command: []string{"/bin/sh", "-c", "sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		})
		created <- outcome{session: session, err: err}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("PTY create did not reach the start hook")
	}

	snapshot, err := supervisor.Activity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PTY != 1 || snapshot.Exec != 0 || !snapshot.Active() {
		t.Fatalf("activity during PTY create = %+v, want one PTY", snapshot)
	}
	if err := snapshot.RequireIdle(); !errors.Is(err, ErrActiveSessions) {
		t.Fatalf("idle guard during PTY create = %v, want ErrActiveSessions", err)
	}

	unblock()
	select {
	case result := <-created:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer func() { _ = supervisor.Kill(context.Background(), result.session.ID()) }()
	case <-time.After(10 * time.Second):
		t.Fatal("PTY create did not finish")
	}
	snapshot, err = supervisor.Activity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PTY != 1 || snapshot.Exec != 0 {
		t.Fatalf("activity after create handoff = %+v, want exactly one PTY", snapshot)
	}

	noPTY := false
	if _, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{"/nonexistent/nexterm-activity-create"},
		Pty:     &noPTY,
	}); err == nil {
		t.Fatal("exec with a missing executable succeeded")
	}
	snapshot, err = supervisor.Activity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PTY != 1 || snapshot.Exec != 0 {
		t.Fatalf("failed exec left stale activity = %+v", snapshot)
	}
}
