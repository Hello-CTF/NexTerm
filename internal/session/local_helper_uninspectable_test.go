//go:build darwin || linux

package session

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// silentEndpoint listens on the local helper endpoint of stateDir and never
// answers, standing in for a helper that hangs or is not the recorded one.
func silentEndpoint(t *testing.T, stateDir string) {
	t.Helper()
	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	go func() {
		defer close(done)
		var conns []net.Conn
		defer func() {
			for _, conn := range conns {
				_ = conn.Close()
			}
		}()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns = append(conns, conn)
		}
	}()
}

func TestLocalTerminalsOfAnUninspectableHelperReportAnErrorNotAnEmptyList(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, stateDir string){
		"hello timeout": func(t *testing.T, stateDir string) {},
		"pid mismatch": func(t *testing.T, stateDir string) {
			// helper.pid names another process than the one serving the endpoint.
			if err := os.WriteFile(filepath.Join(stateDir, "helper.pid"), []byte(strconv.Itoa(os.Getpid()+1)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			stateDir, err := filepath.Abs(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			prepare(t, stateDir)
			silentEndpoint(t, stateDir)
			service := localhelper.New(localhelper.Config{StateDir: stateDir, Executable: "/nonexistent/helper", Logger: slog.New(slog.DiscardHandler)})
			t.Cleanup(service.Close)
			manager, connected := localHelperManager(t, service)
			descriptors, err := manager.ListRemoteDurable(context.Background(), connected.ID)
			if err == nil {
				t.Fatalf("listing an uninspectable helper returned %v and no error", descriptors)
			}
			var unavailable *localhelper.UnavailableError
			if !errors.As(err, &unavailable) || errors.Is(err, localhelper.ErrNotRunning) {
				t.Fatalf("list error = %v, want a visible unavailable reason", err)
			}
			_, err = manager.RecoverRemoteDurable(context.Background(), "0123456789abcdef0123456789abcdef", OpenTabOptions{
				SessionID: connected.ID, ClientID: "client-a", Detached: true, Cols: 80, Rows: 24,
			})
			if err == nil || errors.Is(err, ErrTabNotFound) {
				t.Fatalf("recover from an uninspectable helper = %v, want its error instead of not found", err)
			}
		})
	}
}
