//go:build unix

package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestProviderConformance(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)

	providers := map[string]base.DurableProvider{
		"embedded": NewProvider(supervisor),
		"remote":   NewRemoteProvider(testClient(t, server)),
	}

	script := `printf 'ready\n'
while IFS= read -r line; do
  case "$line" in
    quit*) exit "${line#quit}" ;;
    *) printf 'in:%s\n' "$line" ;;
  esac
done`

	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			id := ids.New()
			attachment, err := provider.Create(ctx, base.DurableCreateOptions{
				ID:      id,
				Command: []string{"/bin/sh", "-c", script},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
				Cols:    80,
				Rows:    24,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = attachment.Close() }()

			if attachment.ID() != id {
				t.Fatalf("attachment ID = %q, want %q", attachment.ID(), id)
			}
			if attachment.Generation() != 0 {
				t.Fatalf("Generation = %d, want 0", attachment.Generation())
			}
			if err := attachment.CloseWrite(); !errors.Is(err, base.ErrUnsupported) {
				t.Fatalf("CloseWrite = %v, want ErrUnsupported", err)
			}
			if attachment.Stderr() != nil {
				t.Fatal("Stderr must be nil")
			}

			grid, ok := attachment.(interface {
				DurableGrid() (uint32, uint32, bool)
			})
			if !ok {
				t.Fatal("attachment must expose DurableGrid")
			}
			if cols, rows, ok := grid.DurableGrid(); !ok || cols != 80 || rows != 24 {
				t.Fatalf("DurableGrid = %d/%d/%v", cols, rows, ok)
			}

			versions, ok := attachment.(interface {
				DurableVersions() (uint64, uint64, error)
				PersistDurableVersions(uint64, uint64) error
			})
			if !ok {
				t.Fatal("attachment must expose durable versions")
			}
			if err := versions.PersistDurableVersions(6, 1); err != nil {
				t.Fatal(err)
			}
			if err := versions.PersistDurableVersions(2, 9); err != nil {
				t.Fatal(err)
			}
			if event, revision, err := versions.DurableVersions(); err != nil || event != 6 || revision != 9 {
				t.Fatalf("DurableVersions = %d/%d, %v", event, revision, err)
			}

			readUntil(t, attachment, "ready")
			if _, err := attachment.Write([]byte("ping\n")); err != nil {
				t.Fatal(err)
			}
			readUntil(t, attachment, "in:ping")
			if err := attachment.Resize(ctx, 120, 40); err != nil {
				t.Fatal(err)
			}
			if _, err := attachment.Write([]byte("quit3\n")); err != nil {
				t.Fatal(err)
			}
			err = attachment.Wait(ctx)
			var exitErr *base.ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != 3 {
				t.Fatalf("Wait = %v, want exit code 3", err)
			}

			if _, err := provider.Attach(ctx, ids.New()); !errors.Is(err, durable.ErrNotFound) {
				t.Fatalf("Attach unknown = %v, want durable.ErrNotFound", err)
			}
			reattached, err := provider.Attach(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			output := string(readToEOF(t, reattached))
			if !strings.Contains(output, "in:ping") {
				t.Fatalf("reattach replay = %q", output)
			}
			if err := reattached.Close(); err != nil {
				t.Fatal(err)
			}
			if err := reattached.Close(); err != nil {
				t.Fatalf("second Close = %v", err)
			}
		})
	}
}

func TestRemoteProviderSurvivesServerRestartOfStream(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	if _, err := stream.Write([]byte("x")); err == nil {
		t.Fatal("Write on a disconnected stream must fail")
	}
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}
