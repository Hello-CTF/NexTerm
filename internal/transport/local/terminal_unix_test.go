//go:build unix

package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestOpenCommandPTYUnicodeLocaleAndTerm(t *testing.T) {
	dir := t.TempDir()
	name := "中文目录Ω🚀"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	transport := NewWithConfig(Config{
		Shell: "/bin/sh",
		CWD:   dir,
		Env:   map[string]string{"LC_ALL": "", "LC_CTYPE": "", "LANG": ""},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := transport.OpenCommandPTY(ctx, "printf 'TERM=%s\\n' \"$TERM\"; ls -1", base.PTYOptions{Cols: 100, Rows: 30, Term: "xterm-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	output, err := io.ReadAll(channel)
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := string(output)
	if !strings.Contains(text, "TERM=xterm-test") || !strings.Contains(text, name) {
		t.Fatalf("PTY output = %q", text)
	}
}

func TestOpenInteractivePTY(t *testing.T) {
	transport := unixTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := transport.OpenPTY(ctx, base.PTYOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if _, err := channel.Write([]byte("printf 'interactive-ok\\n'; exit\n")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(channel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "interactive-ok") {
		t.Fatalf("PTY output = %q", output)
	}
	if err := channel.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channel.CloseWrite(); err != base.ErrUnsupported {
		t.Fatalf("CloseWrite error = %v", err)
	}
}
