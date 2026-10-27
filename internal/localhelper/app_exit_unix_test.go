//go:build unix

package localhelper

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const appProcessStateEnv = "NEXTERM_LOCALHELPER_APP_STATE"

// TestLocalHelperAppProcess plays the desktop app: it starts the local helper,
// opens a terminal and exits the process without any cleanup.
func TestLocalHelperAppProcess(t *testing.T) {
	stateDir := os.Getenv(appProcessStateEnv)
	if stateDir == "" {
		t.Skip("local helper app process")
	}
	service := New(Config{StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler)})
	service.Start()
	provider, err := service.WaitReady(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := provider.Create(context.Background(), base.DurableCreateOptions{
		ID: ids.New(), Command: []string{"/bin/sh", "-c", "printf 'app-exit-marker\\n'; sleep 300"}, Env: []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	var output []byte
	for !bytes.Contains(output, []byte("app-exit-marker")) {
		count, err := attachment.Read(buffer)
		output = append(output, buffer[:count]...)
		if err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}

func TestLocalTerminalsSurviveTheAppProcessExiting(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Cleanup(func() { _ = exec.Command("pkill", "-9", "-f", supervisor.HelperCommand+" --state-dir "+stateDir).Run() })
	app := exec.Command(os.Args[0], "-test.run=^TestLocalHelperAppProcess$", "-test.count=1")
	app.Env = append(os.Environ(), appProcessStateEnv+"="+stateDir)
	if output, err := app.CombinedOutput(); err != nil {
		t.Fatalf("app process: %v\n%s", err, output)
	}
	running, err := supervisor.InspectHelper(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("the helper did not outlive the app process: %v", err)
	}
	if running.Activity.PTY != 1 {
		t.Fatalf("helper after the app exited = %+v, want its terminal running", running)
	}
	service := New(Config{StateDir: stateDir, Executable: os.Args[0], Logger: slog.New(slog.DiscardHandler)})
	service.Start()
	defer service.Close()
	provider, err := service.WaitReady(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lister := provider.(base.DurableDescriptorLister)
	descriptors, err := lister.ListDurableDescriptors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].Dead {
		t.Fatalf("terminals after the app restarted = %+v", descriptors)
	}
	attachment, err := provider.AttachWithIdentity(context.Background(), descriptors[0].ID, base.DurableIdentity{
		CreatedAt: descriptors[0].CreatedAt, Incarnation: descriptors[0].Incarnation, Generation: descriptors[0].Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Close() }()
	done := make(chan []byte, 1)
	go func() {
		var output []byte
		buffer := make([]byte, 4096)
		for !bytes.Contains(output, []byte("app-exit-marker")) {
			count, err := attachment.Read(buffer)
			output = append(output, buffer[:count]...)
			if err != nil && err != io.EOF {
				break
			}
		}
		done <- output
	}()
	select {
	case output := <-done:
		if !bytes.Contains(output, []byte("app-exit-marker")) {
			t.Fatalf("reattached output = %q", output)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("reattach did not replay the terminal output")
	}
	if err := attachment.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestACrashingHelperBinaryOpensTheBreaker(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	service := New(Config{
		StateDir: stateDir, Executable: "/bin/false", Logger: slog.New(slog.DiscardHandler),
		BreakerThreshold: 2, Backoff: []time.Duration{10 * time.Millisecond},
	})
	service.Start()
	defer service.Close()
	if _, err := service.WaitReady(context.Background(), 10*time.Millisecond); err == nil {
		t.Fatal("a crashing helper reported ready")
	}
	deadline := time.Now().Add(30 * time.Second)
	for service.Status().State != StateFailed {
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want the breaker open", service.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := service.WaitReady(context.Background(), time.Second); err == nil {
		t.Fatal("a failed helper reported ready")
	}
}
