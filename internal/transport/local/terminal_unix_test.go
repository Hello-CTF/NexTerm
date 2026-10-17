//go:build unix

package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
	channel, err := transport.OpenCommandPTY(ctx, "printf 'TERM=%s\\n' \"$TERM\"; printf '%s\\n' *", base.PTYOptions{Cols: 100, Rows: 30, Term: "xterm-test"})
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

type localRetryChannel struct {
	base.Channel
	mu       sync.Mutex
	failures int
	err      error
	calls    int
}

func (c *localRetryChannel) Close() error {
	c.mu.Lock()
	c.calls++
	if c.failures > 0 {
		c.failures--
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	return c.Channel.Close()
}

func (c *localRetryChannel) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestTransportCloseRetriesTrackedChannelFailures(t *testing.T) {
	transport := unixTransport(t)
	channel, err := transport.OpenPTY(context.Background(), base.PTYOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tracked := channel.(*trackedChannel)
	underlying := tracked.Channel
	injected := errors.New("injected tracked cleanup failure")
	retry := &localRetryChannel{Channel: underlying, failures: 2, err: injected}
	tracked.Channel = retry
	t.Cleanup(func() { _ = underlying.Close() })
	remaining := func() int {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		return len(transport.channels)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := transport.Close(); !errors.Is(err, injected) {
			t.Fatalf("Close attempt %d error = %v, want injected failure", attempt, err)
		}
		if got := remaining(); got != 1 {
			t.Errorf("tracked channels after failed Close attempt %d = %d, want retained channel", attempt, got)
		}
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("third Close error = %v", err)
	}
	if got := remaining(); got != 0 {
		t.Fatalf("tracked channels after successful Close = %d, want 0", got)
	}
	if err := channel.Close(); err != nil {
		t.Fatalf("tracked Close after transport cleanup = %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("idempotent transport Close = %v", err)
	}
	if got := retry.callCount(); got != 3 {
		t.Fatalf("underlying Close calls = %d, want two failures and one success", got)
	}
}
