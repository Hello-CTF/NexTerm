//go:build unix

package supervisor

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const testReadTimeout = 30 * time.Second

func testSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state"), CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
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
