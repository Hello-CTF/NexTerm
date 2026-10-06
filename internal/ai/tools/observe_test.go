package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRing struct {
	mu     sync.Mutex
	base   uint64
	data   []byte
	latest uint64
}

func newFakeRing(base uint64, data string) *fakeRing {
	return &fakeRing{base: base, data: []byte(data), latest: base + uint64(len(data))}
}

func (f *fakeRing) since(seq uint64, maxBytes int) ([]byte, uint64, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := seq
	if start < f.base {
		start = f.base
	}
	if start > f.latest {
		start = f.latest
	}
	n := int(f.latest - start)
	if maxBytes > 0 && n > maxBytes {
		n = maxBytes
	}
	from := start - f.base
	return append([]byte(nil), f.data[from:from+uint64(n)]...), start, f.latest, nil
}

func (f *fakeRing) append(data string) {
	f.mu.Lock()
	f.data = append(f.data, data...)
	f.latest += uint64(len(data))
	f.mu.Unlock()
}

func anchoredTerminal(ring *fakeRing) *fakeTerminal {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Seq: ring.latest, Cols: 80, Rows: 24}}
	terminal.outputSinceFunc = ring.since
	return terminal
}

func TestReadScreenSinceReturnsOnlyNewOutput(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(ring)})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"since_seq":5}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "新输出（序号 5 → 11）") {
		t.Fatalf("header=%q", result.Text)
	}
	if !strings.Contains(result.Text, "world") || strings.Contains(result.Text, "hello") {
		t.Fatalf("diff leaked old content: %q", result.Text)
	}
	if result.Truncated {
		t.Fatalf("unexpected truncation: %+v", result)
	}
}

func TestReadScreenSincePagesAndFlagsMoreOutput(t *testing.T) {
	ring := newFakeRing(0, "hello world")
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(ring)})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"since_seq":3,"max_bytes":4}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "输出未读完") || !result.Truncated {
		t.Fatalf("paging hint missing: %+v", result)
	}
	if !strings.Contains(result.Text, "lo w") {
		t.Fatalf("paged diff=%q", result.Text)
	}
}

func TestReadScreenSinceFlagsExpiredAnchor(t *testing.T) {
	ring := newFakeRing(8, "world")
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(ring)})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"since_seq":5}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "锚点已过期") || !strings.Contains(result.Text, "序号 8 → 13") {
		t.Fatalf("expired anchor note missing: %q", result.Text)
	}
}

func TestReadScreenSinceValidatesMaxBytes(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(newFakeRing(0, "x"))})
	for _, args := range []string{`{"since_seq":1,"max_bytes":-1}`, `{"since_seq":1,"max_bytes":123456789}`} {
		result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
			Call{ID: "r", Name: "read_screen", Args: json.RawMessage(args)}, nil)
		if result.OK || !strings.Contains(result.Text, "max_bytes") {
			t.Fatalf("args=%s result=%+v", args, result)
		}
	}
}

func TestReadScreenFullIncludesSeqAnchorAndAltScreen(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Seq: 42, AltScreen: true}}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "屏幕序号 42") || !strings.Contains(result.Text, "AltScreen") {
		t.Fatalf("header=%q", result.Text)
	}
}

func TestWaitForSinceSeqIgnoresOldContent(t *testing.T) {
	ring := newFakeRing(0, "DEPLOY TOKEN\r\n")
	terminal := anchoredTerminal(ring)
	terminal.screen.Text = "$ DEPLOY TOKEN"
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"DEPLOY","timeout_ms":80,"since_seq":12}`)}, nil)
	if result.OK || result.ExitCode != 1 {
		t.Fatalf("old content false-matched: %+v", result)
	}
	if !strings.Contains(result.Text, "新输出") {
		t.Fatalf("timeout text=%q", result.Text)
	}
}

func TestWaitForSinceSeqMatchesOnlyNewOutput(t *testing.T) {
	ring := newFakeRing(0, "old\r\n")
	terminal := anchoredTerminal(ring)
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	go func() {
		time.Sleep(15 * time.Millisecond)
		ring.append("build DEPLOY done\r\n")
	}()
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"DEPLOY","timeout_ms":3000,"since_seq":5}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "模式已出现（新输出，屏幕序号 ") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestWaitForSinceSeqCancellation(t *testing.T) {
	ring := newFakeRing(0, "old\r\n")
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(ring), PollInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()
	result := registry.Execute(ctx, "job", Scope{TabID: "tab"},
		Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"never","timeout_ms":60000,"since_seq":4}`)}, nil)
	if result.OK || !strings.Contains(result.Text, context.Canceled.Error()) {
		t.Fatalf("result=%+v", result)
	}
}

func TestAnchoredReadsFailWithoutTerminalSupport(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: legacyTerminal{}})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"since_seq":3}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "不支持锚定输出读取") {
		t.Fatalf("read_screen result=%+v", result)
	}
	result = registry.Execute(context.Background(), "job", Scope{TabID: "tab"},
		Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"x","since_seq":3}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "不支持锚定输出读取") {
		t.Fatalf("wait_for result=%+v", result)
	}
}

func TestSendKeysReturnsScreenAnchor(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Seq: 42}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"},
		Call{ID: "k", Name: "send_keys", Args: json.RawMessage(`{"keys":"ls","enter":true}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "已发送（屏幕序号 42）") {
		t.Fatalf("result=%+v", result)
	}
}

func TestRenderOutputDiffStripsEscapeSequences(t *testing.T) {
	lines := renderOutputDiff([]byte("\x1b[31merror\x1b[0m\r\nline2\r\nabc\rXY"), 80, 24)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("escape leaked: %q", joined)
	}
	if !strings.Contains(joined, "error") || !strings.Contains(joined, "line2") {
		t.Fatalf("content missing: %q", joined)
	}
	if lines[len(lines)-1] != "XYc" {
		t.Fatalf("carriage return overwrite wrong: %q", lines)
	}
}

type legacyTerminal struct{}

func (legacyTerminal) Snapshot(context.Context, string) (Screen, error) {
	return Screen{Text: "$ ", Seq: 9}, nil
}
func (legacyTerminal) Write(context.Context, string, []byte) error { return nil }
