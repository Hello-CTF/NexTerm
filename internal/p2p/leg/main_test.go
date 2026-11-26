package leg

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestMain fails the run if quic-go or pion wrote to the standard logger,
// which in the agent and desktop would end up on stderr.
func TestMain(m *testing.M) {
	var logs lockedBuffer
	log.SetOutput(&logs)
	code := m.Run()
	if out := logs.String(); out != "" {
		fmt.Fprintf(os.Stderr, "unexpected standard logger output:\n%s", out)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// TestHelperDoesNotLinkP2PStack keeps pion and quic-go out of the supervisor
// helper (design 1.2), which is a long-lived process holding PTYs.
func TestHelperDoesNotLinkP2PStack(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		cmd := exec.Command(goTool, "list", "-deps", "github.com/Hello-CTF/NexTerm/cmd/nexterm-helper")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list for %s: %v", goos, err)
		}
		deps := strings.Fields(string(out))
		if len(deps) == 0 {
			t.Fatalf("go list for %s returned no packages", goos)
		}
		for _, dep := range deps {
			for _, banned := range []string{"github.com/pion/", "github.com/quic-go/", "github.com/Hello-CTF/NexTerm/internal/p2p"} {
				if strings.HasPrefix(dep, banned) {
					t.Errorf("%s helper links %s", goos, dep)
				}
			}
		}
	}
}
