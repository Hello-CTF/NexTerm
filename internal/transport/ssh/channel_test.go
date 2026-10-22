package ssh

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecChannelHalfCloseIsIdempotentAndDrainsOutput(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	channel, err := client.OpenExec(ctx, "stream", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	payload := []byte("offline-binary\x00payload")
	if _, err := channel.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatalf("second CloseWrite = %v", err)
	}
	output, err := io.ReadAll(channel)
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]byte("stream:"), payload...); string(output) != string(want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if err := channel.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if server.cols.Load() != 0 || server.rows.Load() != 0 {
		t.Fatalf("exec unexpectedly requested a PTY: %dx%d", server.cols.Load(), server.rows.Load())
	}
}

func TestExecChannelReportsMissingExitStatusAfterHalfClose(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	channel, err := client.OpenExec(ctx, "sleep", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if err := channel.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := channel.Wait(ctx); !errors.Is(err, base.ErrExitStatusMissing) {
		t.Fatalf("Wait = %v, want missing exit status", err)
	}
}

func TestExecChannelPreservesNonzeroExitStatus(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	channel, err := client.OpenExec(ctx, "exit42", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	err = channel.Wait(ctx)
	if code, ok := base.ExitCode(err); !ok || code != 42 {
		t.Fatalf("Wait = %v, want exit 42", err)
	}
	if err := channel.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite after exit = %v", err)
	}
}
