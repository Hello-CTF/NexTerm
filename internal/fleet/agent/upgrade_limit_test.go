package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/idleread"
)

// A release host that streams more than the allowed size must be cut off before the
// data fills the disk; the download fails and nothing is left at the destination.
func TestUpgradeDownloadRejectsOversizedBody(t *testing.T) {
	previous := maxUpgradeDownloadBytes
	maxUpgradeDownloadBytes = 1024
	t.Cleanup(func() { maxUpgradeDownloadBytes = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	t.Cleanup(server.Close)
	upgrader := &Upgrader{BaseURL: server.URL, Executable: filepath.Join(t.TempDir(), "nexterm-agent")}
	destination := filepath.Join(t.TempDir(), "asset")
	if err := upgrader.download(context.Background(), "v1.2.3/asset", destination); err == nil {
		t.Fatal("oversized download was accepted")
	}
}

// A release download is bounded by how long it stalls, not by its total duration: a slow transfer that keeps
// delivering data completes, one that stops sending fails with the idle cause and leaves no partial file.
func TestUpgradeDownloadIdleTimeout(t *testing.T) {
	previous := upgradeDownloadIdleTimeout
	upgradeDownloadIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { upgradeDownloadIdleTimeout = previous })
	const chunks = 10
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		for range chunks {
			_, _ = w.Write([]byte("x"))
			flusher.Flush()
			if r.URL.Path == "/v1.2.3/stalled" {
				select {
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	upgrader := &Upgrader{BaseURL: server.URL, Executable: filepath.Join(t.TempDir(), "nexterm-agent")}

	slow := filepath.Join(t.TempDir(), "slow")
	if err := upgrader.download(context.Background(), "v1.2.3/slow", slow); err != nil {
		t.Fatalf("slow but progressing download failed: %v", err)
	}
	if info, err := os.Stat(slow); err != nil || info.Size() != chunks {
		t.Fatalf("slow download = %v, %v", info, err)
	}

	stalled := filepath.Join(t.TempDir(), "stalled")
	err := upgrader.download(context.Background(), "v1.2.3/stalled", stalled)
	var idle *idleread.IdleError
	if !errors.As(err, &idle) {
		t.Fatalf("stalled download error = %v, want an idle timeout", err)
	}
	if _, statErr := os.Stat(stalled); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a stalled download must not leave a partial file")
	}
}
