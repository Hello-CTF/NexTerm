package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/idleread"
)

// The update download is bounded by stalls, not by total time: a slow transfer that keeps delivering data completes,
// a stalled one fails with the idle cause.
func TestDownloadUsesIdleTimeout(t *testing.T) {
	previous := downloadIdleTimeout
	downloadIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { downloadIdleTimeout = previous })
	const chunks = 10
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		for range chunks {
			_, _ = w.Write([]byte("x"))
			flusher.Flush()
			if r.URL.Path == "/stalled" {
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
	manager := &Manager{}
	dir := t.TempDir()

	slow := filepath.Join(dir, "slow")
	if err := manager.download(context.Background(), Asset{Name: "slow", BrowserDownloadURL: server.URL + "/slow"}, slow, "task"); err != nil {
		t.Fatalf("slow but progressing download failed: %v", err)
	}
	if info, err := os.Stat(slow); err != nil || info.Size() != chunks {
		t.Fatalf("slow download = %v, %v", info, err)
	}

	err := manager.download(context.Background(), Asset{Name: "stalled", BrowserDownloadURL: server.URL + "/stalled"}, filepath.Join(dir, "stalled"), "task")
	var idle *idleread.IdleError
	if !errors.As(err, &idle) {
		t.Fatalf("stalled download error = %v, want an idle timeout", err)
	}
}
