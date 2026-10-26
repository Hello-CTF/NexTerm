//go:build unix

package supervisor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const testReadTimeout = 30 * time.Second

func testSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	// Flow-control tests stream floods far beyond the default history limit
	// to slow consumers; they must not see eviction gaps. History tests pass
	// their own per-session limit.
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state"), CommandTimeout: 5 * time.Second, HistoryLimit: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
}

func testClient(t *testing.T, server *Server) *Client {
	t.Helper()
	return NewClient(server.SocketPath(), server.supervisor.StateDir())
}

func testScriptSession(t *testing.T, supervisor *Supervisor, script string) *Session {
	t.Helper()
	return testScriptSessionID(t, supervisor, ids.New(), script)
}

func testScriptSessionID(t *testing.T, supervisor *Supervisor, id, script string) *Session {
	t.Helper()
	session, err := supervisor.Create(context.Background(), CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", script},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func unsetTerminalEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

type readCloser interface {
	io.Reader
	Close() error
}

func readUntil(t *testing.T, reader readCloser, marker string) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	found := make(chan result, 1)
	go func() {
		var buffer strings.Builder
		chunk := make([]byte, 4096)
		for {
			count, err := reader.Read(chunk)
			if count > 0 {
				buffer.Write(chunk[:count])
				if strings.Contains(buffer.String(), marker) {
					found <- result{data: []byte(buffer.String())}
					return
				}
			}
			if err != nil {
				found <- result{data: []byte(buffer.String()), err: err}
				return
			}
		}
	}()
	select {
	case result := <-found:
		if result.err != nil {
			t.Fatalf("read until %q: %v (data so far %q)", marker, result.err, result.data)
		}
		return result.data
	case <-time.After(testReadTimeout):
		_ = reader.Close()
		t.Fatalf("timed out waiting for %q", marker)
		return nil
	}
}

func readToEOF(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(reader)
		done <- result{data: data, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read to EOF: %v (data %q)", result.err, result.data)
		}
		return result.data
	case <-time.After(testReadTimeout):
		t.Fatal("timed out reading to EOF")
		return nil
	}
}

// readRecording returns the retained recorded output of a session, the
// segments concatenated in offset order.
func readRecording(stateDir, id string) ([]byte, error) {
	recorded := loadHistory(sessionDir(stateDir, id), DefaultHistoryLimit)
	if err := recorded.usable(); err != nil {
		return nil, err
	}
	base, next := recorded.bounds()
	data := make([]byte, next-base)
	count, _, err := recorded.readAt(base, data)
	return data[:count], err
}

// closeRecordingWriter closes the active segment so the next recorded chunk
// fails like a lost disk.
func closeRecordingWriter(session *Session) error {
	session.history.mu.Lock()
	defer session.history.mu.Unlock()
	return session.history.active.Close()
}

func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

func assertOrderedOnce(t *testing.T, output string, markers ...string) {
	t.Helper()
	position := 0
	for _, marker := range markers {
		index := strings.Index(output[position:], marker)
		if index < 0 {
			t.Fatalf("marker %q missing after byte %d in %q", marker, position, output)
		}
		absolute := position + index
		if count := countOccurrences(output, marker); count != 1 {
			t.Fatalf("marker %q appears %d times in %q", marker, count, output)
		}
		position = absolute + len(marker)
	}
}
