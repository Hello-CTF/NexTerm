//go:build unix

package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func TestHelperLogRecordsStartCreateAndShutdown(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunHelper(ctx, HelperConfig{StateDir: stateDir}) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunHelper: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("RunHelper did not stop")
		}
	}
	t.Cleanup(stop)

	endpoint, err := HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(endpoint, stateDir)
	var created Info
	deadline := time.Now().Add(10 * time.Second)
	for {
		callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
		created, err = client.Create(callCtx, CreateOptions{
			ID: ids.New(), Command: []string{"/bin/sh", "-c", "exit 3"}, Dir: stateDir, Cols: 80, Rows: 24,
		})
		callCancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("create: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	waitLog := func(want string) string {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			raw, _ := os.ReadFile(filepath.Join(stateDir, helperLogName))
			if strings.Contains(string(raw), want) {
				return string(raw)
			}
		}
		raw, _ := os.ReadFile(filepath.Join(stateDir, helperLogName))
		t.Fatalf("helper log lacks %q:\n%s", want, raw)
		return ""
	}
	waitLog("event=session_exit id=" + created.ID + " exit_code=3")
	stop()
	log := waitLog("event=shutdown_done")
	for _, want := range []string{
		"event=start ", "protocol=" + "3", "env_path=", "exe=",
		"event=ready ",
		"event=session_create id=" + created.ID + " kind=pty shell=/bin/sh",
		"shell_source=", "path_source=", "durable=yes",
		"event=shutdown_begin",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("helper log lacks %q:\n%s", want, log)
		}
	}
	info, err := os.Stat(filepath.Join(stateDir, helperLogName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("helper log mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestHelperLogRotatesAndRedacts(t *testing.T) {
	dir := t.TempDir()
	l := newHelperLog(dir)
	defer l.close()
	l.event("probe", "url", "https://user:hunter2@example.com/x", "spaced", "a b")
	raw, _ := os.ReadFile(filepath.Join(dir, helperLogName))
	if strings.Contains(string(raw), "hunter2") || !strings.Contains(string(raw), `spaced="a b"`) {
		t.Fatalf("unexpected line: %s", raw)
	}
	filler := strings.Repeat("x", 1024)
	for i := 0; i < 1200; i++ {
		l.event("fill", "data", filler)
	}
	for _, name := range []string{helperLogName, helperLogName + ".1"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > helperLogMaxBytes || info.Mode().Perm() != 0o600 {
			t.Errorf("%s size=%d mode=%v", name, info.Size(), info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, helperLogName+".2")); err == nil {
		t.Error("more than one rollover file")
	}
}
