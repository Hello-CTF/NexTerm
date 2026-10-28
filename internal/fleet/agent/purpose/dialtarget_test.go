package purpose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandSocketPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := expandSocketPath("~/.docker/run/docker.sock"), filepath.Join(home, ".docker/run/docker.sock"); got != want {
		t.Fatalf("home path = %q; want %q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got, want := expandSocketPath("$XDG_RUNTIME_DIR/docker.sock"), "/run/user/1000/docker.sock"; got != want {
		t.Fatalf("runtime path = %q; want %q", got, want)
	}
	if got := expandSocketPath("/var/run/docker.sock"); got != "/var/run/docker.sock" {
		t.Fatalf("absolute path changed: %q", got)
	}
}
