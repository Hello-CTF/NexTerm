package ssh

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestClientOpenPTYSendsConfiguredEnv(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.envReqs = make(chan [2]string, 8)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.Env = map[string]string{"B_VAR": "2", "A_VAR": "1"}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pty, err := client.OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24, ExpectedGeneration: client.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	for _, want := range [][2]string{{"A_VAR", "1"}, {"B_VAR", "2"}} {
		select {
		case got := <-server.envReqs:
			if got != want {
				t.Fatalf("env request = %v, want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("env request %v was not sent", want)
		}
	}
	ready := make([]byte, 5)
	if _, err := io.ReadFull(pty, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("PTY ready = %q, %v", ready, err)
	}
}

func TestClientOpenPTYEnvRejectionDoesNotFailShell(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.Env = map[string]string{"A_VAR": "1"}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pty, err := client.OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24, ExpectedGeneration: client.Generation()})
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(pty, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("PTY ready = %q, %v", ready, err)
	}
}
