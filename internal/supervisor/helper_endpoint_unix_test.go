//go:build unix

package supervisor

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHelperEndpointKeepsDesktopTempDirLayout(t *testing.T) {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		t.Skip("mobile redirects the temp dir")
	}
	stateDir := t.TempDir()
	endpoint, err := helperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", os.Geteuid(), stateDir)))
	want := filepath.Join(os.TempDir(), fmt.Sprintf("nexterm-sup-%x", sum[:8]), fmt.Sprintf("supervisor-v%d.sock", ProtocolVersion))
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}
