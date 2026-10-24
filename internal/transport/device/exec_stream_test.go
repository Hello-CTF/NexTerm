//go:build !windows

package device

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestOpenExecStreamsOrderedOutputAndExitStatus(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	channel, err := transport.OpenExec(context.Background(), `printf out; printf err >&2; exit 3`, base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	ordered := channel.(base.OrderedOutput)
	var stdout, stderr string
	for {
		event, err := ordered.NextOutput(context.Background())
		if event.Stderr {
			stderr += string(event.Data)
		} else {
			stdout += string(event.Data)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if stdout != "out" || stderr != "err" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	waitErr := channel.Wait(context.Background())
	if code, ok := base.ExitCode(waitErr); !ok || code != 3 {
		t.Fatalf("Wait()=%v, want exit status 3", waitErr)
	}
}

func TestOpenExecCloseStopsLongRunningCommand(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 1)
	channel, err := transport.OpenExec(context.Background(), `printf ready; sleep 60`, base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	event, err := channel.(base.OrderedOutput).NextOutput(ctx)
	if err != nil || string(event.Data) != "ready" {
		t.Fatalf("first output=%q err=%v", event.Data, err)
	}
	closed := make(chan struct{})
	go func() { _ = channel.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not stop the running command")
	}
	if _, err := channel.Write([]byte("x")); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Write err=%v, want ErrUnsupported", err)
	}
}

func TestOpenExecRejectsStaleGenerationAndClosedTransport(t *testing.T) {
	rig := newPurposeRig(t)
	transport := New("dev-1", rig.digest, rig.dial, 2)
	if _, err := transport.OpenExec(context.Background(), "true", base.ExecOptions{ExpectedGeneration: 1}); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("stale generation err=%v", err)
	}
	_ = transport.Close()
	if _, err := transport.OpenExec(context.Background(), "true", base.ExecOptions{}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("closed transport err=%v", err)
	}
}
