//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func limitedSupervisor(t *testing.T, limits Limits) *Supervisor {
	t.Helper()
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state"), CommandTimeout: 5 * time.Second, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
}

func createSleeper(supervisor *Supervisor) (*Session, error) {
	return supervisor.Create(context.Background(), CreateOptions{
		ID: ids.New(), Command: []string{"/bin/sh", "-c", "sleep 60"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
}

func TestSupervisorRefusesCreatesBeyondTheSessionLimit(t *testing.T) {
	supervisor := limitedSupervisor(t, Limits{MaxSessions: 2})
	first, err := createSleeper(supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createSleeper(supervisor); err != nil {
		t.Fatal(err)
	}
	if _, err := createSleeper(supervisor); !errors.Is(err, ErrLimit) {
		t.Fatalf("third create = %v, want ErrLimit", err)
	}
	identity := first.Identity()
	if err := supervisor.kill(context.Background(), first.ID(), &identity); err != nil {
		t.Fatal(err)
	}
	if _, err := createSleeper(supervisor); err != nil {
		t.Fatalf("create after a session ended = %v; ended sessions must not count", err)
	}
}

func TestSupervisorRefusesACreateStormAndRecoversAfterTheWindow(t *testing.T) {
	supervisor := limitedSupervisor(t, Limits{MaxCreates: 3, CreateWindow: time.Minute})
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	supervisor.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	for index := 0; index < 3; index++ {
		session, err := createSleeper(supervisor)
		if err != nil {
			t.Fatal(err)
		}
		identity := session.Identity()
		if err := supervisor.kill(context.Background(), session.ID(), &identity); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := createSleeper(supervisor); !errors.Is(err, ErrLimit) {
		t.Fatalf("fourth create within the window = %v, want ErrLimit", err)
	}
	mu.Lock()
	now = now.Add(time.Minute + time.Second)
	mu.Unlock()
	if _, err := createSleeper(supervisor); err != nil {
		t.Fatalf("create after the window = %v", err)
	}
}

func TestSupervisorLimitReachesRemoteClientsAsTypedErrors(t *testing.T) {
	supervisor := limitedSupervisor(t, Limits{MaxSessions: 1})
	runDir, err := os.MkdirTemp("/tmp", "nx-limit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := NewServer(supervisor, filepath.Join(runDir, "supervisor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	provider := NewRemoteProvider(testClient(t, server))
	options := base.DurableCreateOptions{Command: []string{"/bin/sh", "-c", "sleep 60"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"}}
	attachment, err := provider.Create(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attachment.Close() })
	_, err = provider.Create(context.Background(), options)
	if !errors.Is(err, durable.ErrLimit) || !errors.Is(err, ErrLimit) {
		t.Fatalf("create over the limit = %v, want durable.ErrLimit wrapping ErrLimit", err)
	}
	if errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("a limit refusal must not read as an unavailable helper: %v", err)
	}
}

func TestHelperArgsCarryLimits(t *testing.T) {
	limits := Limits{MaxSessions: 7, MaxCreates: 9, CreateWindow: 3 * time.Second}
	config, err := parseHelperArgs(append([]string{"--state-dir", "/tmp/x"}, limits.args()...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.Limits != limits {
		t.Fatalf("parsed limits = %+v, want %+v", config.Limits, limits)
	}
	for _, bad := range [][]string{{"--max-sessions", "0"}, {"--max-creates", "x"}, {"--create-window", "-1s"}} {
		if _, err := parseHelperArgs(append([]string{"--state-dir", "/tmp/x"}, bad...), nil); err == nil {
			t.Fatalf("limit args %v were accepted", bad)
		}
	}
}

func TestSupervisorDoesNotRateLimitExecSessions(t *testing.T) {
	supervisor := limitedSupervisor(t, Limits{MaxCreates: 1, CreateWindow: time.Hour})
	client := testClient(t, testServer(t, supervisor))
	for index := 0; index < 3; index++ {
		if _, _, _, _, err := client.Exec(context.Background(), ExecSpec{Command: []string{"/bin/true"}}); err != nil {
			t.Fatalf("exec %d = %v; exec sessions must not hit the create rate", index, err)
		}
	}
}
