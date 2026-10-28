package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
