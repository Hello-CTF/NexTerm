package pprof

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestResolveAddr(t *testing.T) {
	getenv := func(key string) string {
		if key == EnvAddr {
			return " 127.0.0.1:6061 "
		}
		return ""
	}
	if got := ResolveAddr("127.0.0.1:6060", getenv); got != "127.0.0.1:6060" {
		t.Fatalf("flag value = %q", got)
	}
	if got := ResolveAddr(" ", getenv); got != "127.0.0.1:6061" {
		t.Fatalf("env fallback = %q", got)
	}
	if got := ResolveAddr("", func(string) string { return "" }); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := ResolveAddr("", nil); got != "" {
		t.Fatalf("nil getenv = %q", got)
	}
}

func TestNormalizeAddr(t *testing.T) {
	allowed := map[string]string{
		"127.0.0.1:6060": "127.0.0.1:6060",
		":6060":          "127.0.0.1:6060",
		"localhost:6060": "localhost:6060",
		"[::1]:6060":     "[::1]:6060",
		"0.0.0.0:6060":   "0.0.0.0:6060",
		"[::]:6060":      "[::]:6060",
		" 127.0.0.1:0 ":  "127.0.0.1:0",
	}
	for input, want := range allowed {
		got, err := NormalizeAddr(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeAddr(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if got, err := NormalizeAddr(""); err != nil || got != "" {
		t.Fatalf("empty = %q, %v", got, err)
	}
	for _, input := range []string{
		"192.168.1.10:6060",
		"10.0.0.2:6060",
		"example.com:6060",
		"garbage",
		"6060",
	} {
		if got, err := NormalizeAddr(input); err == nil {
			t.Fatalf("NormalizeAddr(%q) = %q, want error", input, got)
		}
	}
}

func TestStartDisabledByDefault(t *testing.T) {
	server, err := Start(testLogger(), "")
	if err != nil || server != nil {
		t.Fatalf("Start(\"\") = %v, %v", server, err)
	}
	if server.Addr() != "" {
		t.Fatal("nil server must report no address")
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartRejectsNonLoopback(t *testing.T) {
	if _, err := Start(testLogger(), "192.168.1.10:6060"); err == nil {
		t.Fatal("non-loopback address was accepted")
	}
}

func TestStartServesEndpoints(t *testing.T) {
	previousMutex := runtime.SetMutexProfileFraction(-1)
	runtime.SetMutexProfileFraction(previousMutex)
	server, err := Start(testLogger(), ":0")
	if err != nil {
		t.Fatal(err)
	}
	if rate := runtime.SetMutexProfileFraction(-1); rate != 1 {
		t.Fatalf("mutex profile fraction after Start = %d, want 1", rate)
	}
	addr := server.Addr()
	if addr == "" || !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want a 127.0.0.1 address", addr)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/cmdline"} {
		response, err := http.Get(fmt.Sprintf("http://%s%s", addr, path))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("GET %s = %d, %d bytes, %v", path, response.StatusCode, len(body), err)
		}
	}
}
