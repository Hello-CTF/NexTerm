package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestScreenTextHasNoANSIResidue(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("\x1b[2J\x1b[H"))
	tab.Feed([]byte("hello \x1b[31;1mworld\x1b[0m\r\n"))
	tab.Feed([]byte("$ \x1b[?25lhidden"))
	text := tab.ScreenText()
	if !strings.Contains(text, "hello world") {
		t.Fatalf("text = %q", text)
	}
	if !strings.Contains(text, "$") {
		t.Fatalf("prompt missing: %q", text)
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("ANSI residue: %q", text)
	}
	if tab.State().CursorVisible {
		t.Fatal("?25l should hide cursor in State")
	}
}

func TestCursorPositionReported(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("abc"))
	snap := tab.Snapshot()
	if snap.Rows != 24 || snap.Cols != 80 || snap.CursorRow != 0 || snap.CursorCol != 3 {
		t.Fatalf("snap = %+v", snap)
	}
	if len(snap.Lines) == 0 || snap.Lines[0] != "abc" {
		t.Fatalf("lines = %q", snap.Lines)
	}
}

func TestAltScreenDetection(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("\x1b[?1049h"))
	if !tab.Snapshot().AltScreen || !tab.State().AltScreen {
		t.Fatal("expected alt screen")
	}
	tab.Feed([]byte("\x1b[?1049l"))
	if tab.Snapshot().AltScreen {
		t.Fatal("expected main screen")
	}
}

func TestIdleDetection(t *testing.T) {
	clock := newFakeClock()
	tab := newTestTab(t, WithClock(clock.now))
	tab.Feed([]byte("boom\r\n"))
	if tab.IsIdle(5 * time.Second) {
		t.Fatal("just fed: not idle")
	}
	clock.advance(10 * time.Millisecond)
	if !tab.IsIdle(time.Millisecond) {
		t.Fatal("should be idle at 1ms quiet")
	}
	if got := tab.LastOutputAgo(); got != 10*time.Millisecond {
		t.Fatalf("ago = %v", got)
	}
	if tab.Snapshot().LastOutputMsAgo != 10 {
		t.Fatalf("snapshot ago = %d", tab.Snapshot().LastOutputMsAgo)
	}
}

func TestTailLinesWithScrollback(t *testing.T) {
	tab := newTestTab(t)
	for i := 0; i < 100; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	tail := tab.TailLines(10)
	if len(tail) != 10 {
		t.Fatalf("tail len = %d: %q", len(tail), tail)
	}
	found := false
	for _, l := range tail {
		if strings.Contains(l, "line-99") {
			found = true
		}
	}
	if !found {
		t.Fatalf("tail missing line-99: %q", tail)
	}
}

func TestTailLinesDecodesCurrentEncoding(t *testing.T) {
	tab := NewTab("t", "s", 80, 24, GBK)
	t.Cleanup(tab.Close)
	raw := encodeForTest(t, simplifiedchinese.GBK, "第一行\r\n第二行\r\n")
	tab.Feed(raw)
	// Tail includes the whole visible grid, so ask for enough lines to
	// reach the content above the empty bottom rows.
	tail := tab.TailLines(30)
	joined := strings.Join(tail, "\n")
	if !strings.Contains(joined, "第一行") || !strings.Contains(joined, "第二行") {
		t.Fatalf("GBK tail garbled: %q", tail)
	}
}

func TestResizeUpdatesStateMachine(t *testing.T) {
	tab := newTestTab(t)
	var got [2]int
	tab.SetResizeHandler(func(cols, rows int) { got = [2]int{cols, rows} })
	if err := tab.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	tab.Feed([]byte("x"))
	snap := tab.Snapshot()
	if snap.Cols != 120 || snap.Rows != 40 || tab.Cols() != 120 || tab.Rows() != 40 {
		t.Fatalf("snap = %+v", snap)
	}
	if got != [2]int{120, 40} {
		t.Fatalf("resize handler got %v", got)
	}
	for _, dims := range [][2]int{{0, 24}, {80, 0}, {1025, 24}, {80, 1025}, {-1, 5}} {
		if err := tab.Resize(dims[0], dims[1]); err == nil {
			t.Fatalf("resize %v should fail", dims)
		}
	}
}

func TestRecordingLifecycle(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "rec.log")
	if err := os.WriteFile(path, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := o.Chunk(ctx, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(ctx, []byte("def")); err != nil {
		t.Fatal(err)
	}
	if tab.RecordingBytes() != 6 {
		t.Fatalf("recorded = %d", tab.RecordingBytes())
	}
	if n := tab.StopRecording(); n != 6 {
		t.Fatalf("stop = %d", n)
	}
	if tab.StopRecording() != 0 {
		t.Fatal("second stop should return 0")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("OLDabcdef")) {
		t.Fatalf("file = %q", data)
	}
	// Direct Feed bypasses recording (it is the state-only path).
	tab.Feed([]byte("zzz"))
	// Restarting resets the counter and keeps appending.
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if tab.RecordingBytes() != 0 {
		t.Fatal("counter should reset")
	}
	if err := o.Chunk(ctx, []byte("g")); err != nil {
		t.Fatal(err)
	}
	if n := tab.StopRecording(); n != 1 {
		t.Fatalf("stop = %d", n)
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(data, []byte("OLDabcdefg")) {
		t.Fatalf("file = %q", data)
	}
}

func TestDumpAndExportLog(t *testing.T) {
	tab := newTestTab(t)
	raw := []byte("log line\x1b[31m\xff\xfe\r\n")
	tab.Feed(raw)
	if got := tab.Dump(0); !bytes.Equal(got, raw) {
		t.Fatalf("dump = %q", got)
	}
	if got := tab.Dump(4); len(got) != 4 {
		t.Fatalf("dump(4) = %q", got)
	}
	path := filepath.Join(t.TempDir(), "export.log")
	n, err := tab.ExportLog(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != uint64(len(raw)) {
		t.Fatalf("exported = %d", n)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, raw) {
		t.Fatalf("export mismatch: %q", data)
	}
	if tab.LatestSeq() != uint64(len(raw)) || tab.BaseSeq() != 0 || tab.DroppedBytes() != 0 {
		t.Fatalf("seq accounting: base=%d latest=%d dropped=%d", tab.BaseSeq(), tab.LatestSeq(), tab.DroppedBytes())
	}
}

func TestDSRAndQueryResponses(t *testing.T) {
	responses := make(chan []byte, 16)
	tab := newTestTab(t, WithResponseHandler(func(p []byte) { responses <- p }))
	expect := func(what string) []byte {
		t.Helper()
		select {
		case r := <-responses:
			return r
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
			return nil
		}
	}
	tab.Feed([]byte("abc"))
	tab.Feed([]byte("\x1b[6n"))
	if got := expect("DSR"); !bytes.Equal(got, []byte("\x1b[1;4R")) {
		t.Fatalf("DSR = %q", got)
	}
	// Split across chunks: the VT parser is incremental too.
	tab.Feed([]byte("\x1b["))
	tab.Feed([]byte("6n"))
	if got := expect("split DSR"); !bytes.Equal(got, []byte("\x1b[1;4R")) {
		t.Fatalf("split DSR = %q", got)
	}
	tab.Feed([]byte("\x1b[c"))
	got := expect("DA")
	if !bytes.HasPrefix(got, []byte("\x1b[?")) || !bytes.HasSuffix(got, []byte("c")) {
		t.Fatalf("DA = %q", got)
	}
}

func TestCloseIdempotentAndFeedAfterClose(t *testing.T) {
	tab := NewTab("t", "s", 80, 24, UTF8)
	tab.Feed([]byte("before"))
	tab.Close()
	tab.Close() // idempotent
	tab.Feed([]byte("after"))
	if got := tab.Dump(0); !bytes.Equal(got, []byte("before")) {
		t.Fatalf("dump = %q", got)
	}
	if err := tab.Resize(100, 30); !errors.Is(err, ErrClosed) {
		t.Fatalf("resize after close = %v", err)
	}
}

func TestScreenHistory(t *testing.T) {
	tab := NewTab("t", "s", 80, 5, UTF8, WithScreenHistory(10))
	t.Cleanup(tab.Close)
	for i := 0; i < 50; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	if got := tab.ScrollbackLen(); got != 10 {
		t.Fatalf("scrollback len = %d", got)
	}
	hist := tab.ScreenHistory(3)
	if len(hist) != 3 {
		t.Fatalf("history = %q", hist)
	}
	// The final CRLF scrolls once more: the grid shows 46..49 plus an
	// empty cursor row, so history ends at 45.
	want := []string{"line-43", "line-44", "line-45"}
	for i := range want {
		if hist[i] != want[i] {
			t.Fatalf("history = %q, want %q", hist, want)
		}
	}
	if all := tab.ScreenHistory(100); len(all) != 10 {
		t.Fatalf("history clamp = %d", len(all))
	}
}

func TestSnapshotLinesSemantics(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("ab  \r\n"))
	snap := tab.Snapshot()
	if len(snap.Lines) == 0 || snap.Lines[0] != "ab" {
		t.Fatalf("lines should trim trailing spaces: %q", snap.Lines)
	}
	// Empty trailing line after the final newline is dropped, Rust-style.
	for i, l := range snap.Lines {
		if i > 0 && l != "" {
			t.Fatalf("unexpected content at %d: %q", i, l)
		}
	}
}

func TestHighVolumeFeedMemoryBounded(t *testing.T) {
	if testing.Short() || raceDetectorEnabled {
		t.Skip("high-volume test skipped in -short and -race")
	}
	tab := newTestTab(t)
	chunk := make([]byte, 65536)
	for i := range chunk {
		chunk[i] = byte(i%251) + 1
	}
	const total = 40 * 1024 * 1024
	start := time.Now()
	for fed := 0; fed < total; fed += len(chunk) {
		tab.Feed(chunk)
	}
	t.Logf("fed %d MiB in %v (%.1f MiB/s)", total/1024/1024, time.Since(start), float64(total)/time.Since(start).Seconds()/1024/1024)
	if got := tab.Dump(1024); len(got) > 1024 {
		t.Fatalf("dump len = %d", len(got))
	}
	if tab.LatestSeq() != total {
		t.Fatalf("latest = %d", tab.LatestSeq())
	}
	if tab.DroppedBytes() != total-ScrollbackBytes {
		t.Fatalf("dropped = %d, want %d", tab.DroppedBytes(), total-ScrollbackBytes)
	}
	if got := len(tab.Dump(0)); got != ScrollbackBytes {
		t.Fatalf("retained = %d, want %d", got, ScrollbackBytes)
	}
	_ = tab.ScreenText()
}

func TestDefaultCapacities(t *testing.T) {
	if ScrollbackBytes != 32*1024*1024 || ScrollbackLines != 100_000 {
		t.Fatal("capacity constants changed")
	}
	tab := newTestTab(t)
	if tab.ring.Cap() != ScrollbackBytes {
		t.Fatalf("ring cap = %d", tab.ring.Cap())
	}
}
