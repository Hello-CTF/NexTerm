package terminal

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestLineHistoryGrowthAndWrap(t *testing.T) {
	history := newLineHistory(1000)
	if got := len(history.lines); got != 256 {
		t.Fatalf("initial backing = %d, want 256", got)
	}
	for i := 0; i < 1000; i++ {
		history.push(fmt.Sprintf("line-%04d", i))
		wantCapacity := 256
		for wantCapacity < min(i+1, 1000) {
			wantCapacity = min(wantCapacity*2, 1000)
		}
		if got := len(history.lines); got != wantCapacity {
			t.Fatalf("backing after %d lines = %d, want %d", i+1, got, wantCapacity)
		}
	}
	if history.head != 0 {
		t.Fatalf("head after growth = %d, want 0", history.head)
	}
	got := history.last(1200)
	if len(got) != 1000 {
		t.Fatalf("full history length = %d", len(got))
	}
	for i, line := range got {
		if want := fmt.Sprintf("line-%04d", i); line != want {
			t.Fatalf("history[%d] = %q, want %q", i, line, want)
		}
	}
	got[0] = "changed"
	if again := history.last(1000); again[0] != "line-0000" {
		t.Fatalf("last aliases its return slice: %q", again[0])
	}
	for i := 1000; i < 1005; i++ {
		history.push(fmt.Sprintf("line-%04d", i))
	}
	if history.len() != 1000 || history.head != 5 {
		t.Fatalf("wrapped size/head = %d/%d", history.len(), history.head)
	}
	wrapped := history.last(1000)
	for i, line := range wrapped {
		if want := fmt.Sprintf("line-%04d", i+5); line != want {
			t.Fatalf("wrapped history[%d] = %q, want %q", i, line, want)
		}
	}
	if history.last(0) != nil || history.last(-1) != nil {
		t.Fatal("non-positive last must return nil")
	}
}

func TestLineHistoryClearAndReuse(t *testing.T) {
	history := newLineHistory(4096)
	for i := 0; i < 5000; i++ {
		history.push(fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 8192)))
	}
	if history.len() != 4096 || len(history.lines) != 4096 {
		t.Fatalf("full size/backing = %d/%d", history.len(), len(history.lines))
	}
	backing := &history.lines[0]
	history.clear()
	if history.len() != 0 || history.last(1) != nil {
		t.Fatal("clear left readable history")
	}
	if got := len(history.lines); got != 4096 {
		t.Errorf("backing after clear = %d, want 4096", got)
	}
	if &history.lines[0] != backing {
		t.Fatal("clear replaced the history backing")
	}
	for i, line := range history.lines {
		if line != "" {
			t.Errorf("slot %d retains %d bytes after clear", i, len(line))
			break
		}
	}
	for i := 5000; i < 6500; i++ {
		history.push(fmt.Sprintf("line-%04d", i))
	}
	if got := len(history.lines); got != 4096 {
		t.Fatalf("backing after reuse = %d, want 4096", got)
	}
	if &history.lines[0] != backing {
		t.Fatal("refill replaced the history backing")
	}
	reused := history.last(2000)
	if len(reused) != 1500 || reused[0] != "line-5000" || reused[len(reused)-1] != "line-6499" {
		t.Fatalf("reuse history boundaries = %d %q %q", len(reused), reused[0], reused[len(reused)-1])
	}
	for i := 6500; i < 9500; i++ {
		history.push(fmt.Sprintf("line-%04d", i))
	}
	wrapped := history.last(5000)
	if len(wrapped) != 4096 || wrapped[0] != "line-5404" || wrapped[len(wrapped)-1] != "line-9499" {
		t.Fatalf("reused wrapped history boundaries = %d %q %q", len(wrapped), wrapped[0], wrapped[len(wrapped)-1])
	}
	if &history.lines[0] != backing {
		t.Fatal("wrapped refill replaced the history backing")
	}
	history.clear()
	if got := len(history.lines); got != 4096 {
		t.Fatalf("backing after second clear = %d, want 4096", got)
	}
	if &history.lines[0] != backing {
		t.Fatal("second clear replaced the history backing")
	}
}

func TestLineHistoryKeepsConfiguredLimit(t *testing.T) {
	history := newLineHistory(ScrollbackLines)
	for i := 0; i < ScrollbackLines+7; i++ {
		history.push(fmt.Sprintf("line-%06d", i))
	}
	if history.len() != ScrollbackLines || len(history.lines) != ScrollbackLines {
		t.Fatalf("size/backing = %d/%d", history.len(), len(history.lines))
	}
	got := history.last(ScrollbackLines)
	if len(got) != ScrollbackLines || got[0] != "line-000007" || got[len(got)-1] != "line-100006" {
		t.Fatalf("100000-line boundaries = %d %q %q", len(got), got[0], got[len(got)-1])
	}
}

func TestScreenHistoryLongLines(t *testing.T) {
	tab := NewTab("long", "s", 120, 40, UTF8, WithScreenHistory(300))
	t.Cleanup(tab.Close)
	line := strings.Repeat("x", 8192) + "\r\n"
	payload := strings.Repeat(line, 20)
	tab.Feed([]byte(payload))
	history := tab.ScreenHistory(1000)
	if len(history) != 300 {
		t.Fatalf("long-line history = %d, want 300", len(history))
	}
	for i, line := range history {
		if len(line) != 120 && len(line) != 32 || strings.Trim(line, "x") != "" {
			t.Fatalf("history[%d] has invalid wrapped content length %d", i, len(line))
		}
	}
	if dump := tab.Dump(0); !bytes.Equal(dump, []byte(payload)) {
		t.Fatalf("raw export = %d bytes, want %d", len(dump), len(payload))
	}
}

func TestScreenHistoryANSICJK(t *testing.T) {
	tab := NewTab("ansi", "s", 120, 40, UTF8, WithScreenHistory(300))
	t.Cleanup(tab.Close)
	var input strings.Builder
	for i := 0; i < 350; i++ {
		fmt.Fprintf(&input, "\x1b[38;5;%dm%04d-中文-终端\x1b[0m\r\n", 16+i%200, i)
	}
	tab.Feed([]byte(input.String()))
	history := tab.ScreenHistory(1000)
	if len(history) != 300 {
		t.Fatalf("ANSI/CJK history = %d, want 300", len(history))
	}
	if history[0] != "0011-中文-终端" || history[len(history)-1] != "0310-中文-终端" {
		t.Fatalf("ANSI/CJK boundaries = %q %q", history[0], history[len(history)-1])
	}
	for _, line := range history {
		if strings.Contains(line, "\x1b") {
			t.Fatalf("ANSI sequence retained in rendered history: %q", line)
		}
	}
	if dump := tab.Dump(0); !bytes.Equal(dump, []byte(input.String())) {
		t.Fatalf("ANSI/CJK raw export = %d bytes, want %d", len(dump), len(input.String()))
	}
}

func TestScreenHistoryAltScreenPreservesMain(t *testing.T) {
	tab := NewTab("alt", "s", 120, 40, UTF8, WithScreenHistory(300))
	t.Cleanup(tab.Close)
	var main strings.Builder
	for i := 0; i < 350; i++ {
		fmt.Fprintf(&main, "main-%04d\r\n", i)
	}
	tab.Feed([]byte(main.String()))
	before := tab.ScreenHistory(1000)
	tab.Feed([]byte("\x1b[?1049h"))
	for i := 0; i < 100; i++ {
		tab.Feed([]byte(fmt.Sprintf("alt-%04d\r\n", i)))
	}
	if got := tab.ScreenHistory(1000); !slices.Equal(got, before) {
		t.Fatal("alt-screen output changed main history")
	}
	tab.Feed([]byte("\x1b[?1049l"))
	if got := tab.ScreenHistory(1000); !slices.Equal(got, before) {
		t.Fatal("leaving alt-screen changed main history")
	}
	for i := 350; i < 400; i++ {
		tab.Feed([]byte(fmt.Sprintf("main-%04d\r\n", i)))
	}
	history := tab.ScreenHistory(1000)
	if len(history) != 300 || history[len(history)-1] != "main-0360" {
		t.Fatalf("main history after alt-screen = %d %q", len(history), history[len(history)-1])
	}
	for _, line := range history {
		if strings.HasPrefix(line, "alt-") {
			t.Fatalf("alt-screen line leaked into main history: %q", line)
		}
	}
}

func TestConcurrentScreenHistoryGrowthAndClear(t *testing.T) {
	tab := NewTab("race", "s", 120, 40, UTF8, WithScreenHistory(2048))
	t.Cleanup(tab.Close)
	start := make(chan struct{})
	errors := make(chan error, 1)
	report := func(err error) {
		select {
		case errors <- err:
		default:
		}
	}
	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				payload := fmt.Sprintf("\x1b[3%dmwriter-%d-%04d-中文\x1b[0m\r\n", writer, writer, i)
				if i%50 == 0 {
					payload += strings.Repeat("x", 8192) + "\r\n"
				}
				tab.Feed([]byte(payload))
			}
		}(writer)
	}
	for reader := 0; reader < 2; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				if got := len(tab.ScreenHistory(3000)); got > 2048 {
					report(fmt.Errorf("history length = %d", got))
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			tab.Feed([]byte("\x1b[3J"))
		}
	}()
	close(start)
	wg.Wait()
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
	backing := &tab.scr.history.lines[0]
	backingLen := len(tab.scr.history.lines)
	tab.Feed([]byte("\x1b[3J"))
	if got := len(tab.scr.history.lines); got != backingLen {
		t.Fatalf("backing after concurrent clear = %d, want %d", got, backingLen)
	}
	if &tab.scr.history.lines[0] != backing {
		t.Fatal("concurrent clear replaced the history backing")
	}
}
