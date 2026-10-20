package terminal

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentResizeConsistency(t *testing.T) {
	tab := newTestTab(t)
	var mu sync.Mutex
	var last [2]int
	calls := 0
	tab.SetResizeHandler(func(cols, rows int) {
		mu.Lock()
		last = [2]int{cols, rows}
		calls++
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := tab.Resize(60+(g*50+i)%40, 20+(g*50+i)%20); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	mu.Lock()
	final, n := last, calls
	mu.Unlock()
	if n != 400 {
		t.Fatalf("handler calls = %d", n)
	}
	if tab.Cols() != final[0] || tab.Rows() != final[1] {
		t.Fatalf("getters %dx%d != last handler %v", tab.Cols(), tab.Rows(), final)
	}
	snap := tab.Snapshot()
	if snap.Cols != final[0] || snap.Rows != final[1] {
		t.Fatalf("grid %dx%d != last handler %v", snap.Cols, snap.Rows, final)
	}
}

func TestEraseScrollbackED3(t *testing.T) {
	tab := NewTab("t", "s", 40, 5, UTF8)
	t.Cleanup(tab.Close)
	for i := 0; i < 30; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	if tab.ScrollbackLen() == 0 {
		t.Fatal("expected history before ED3")
	}
	visibleBefore := tab.ScreenText()
	tab.Feed([]byte("\x1b[3J"))
	if got := tab.ScrollbackLen(); got != 0 {
		t.Fatalf("history after ED3 = %d lines", got)
	}
	if got := tab.ScreenHistory(5); len(got) != 0 {
		t.Fatalf("history lines after ED3 = %q", got)
	}
	if tab.ScreenText() != visibleBefore {
		t.Fatal("ED3 must not touch the visible grid")
	}
	for i := 30; i < 40; i++ {
		tab.Feed([]byte(fmt.Sprintf("line-%d\r\n", i)))
	}
	hist := tab.ScreenHistory(100)
	if len(hist) == 0 {
		t.Fatal("history should accumulate again after ED3")
	}
	for _, line := range hist {
		var idx int
		if _, err := fmt.Sscanf(line, "line-%d", &idx); err != nil {
			t.Fatalf("unparsable history line %q: %v", line, err)
		}
		if idx < 26 {
			t.Fatalf("pre-ED3 history line survived: %q", hist)
		}
	}
}

func TestSplitGraphemeClustersPreserveState(t *testing.T) {
	input := "é combining 👨‍👩‍👧 family 🇨🇳 flag 🚀\r\nnext"
	run := func(chunks [][]byte) Snapshot {
		tab := NewTab("t", "s", 30, 6, UTF8)
		t.Cleanup(tab.Close)
		for _, c := range chunks {
			tab.Feed(c)
		}
		return tab.Snapshot()
	}
	whole := run([][]byte{[]byte(input)})
	if !strings.Contains(whole.Text, "é") {
		t.Fatalf("combining mark lost: %q", whole.Text)
	}
	if !strings.Contains(whole.Text, "👨‍👩‍👧") || !strings.Contains(whole.Text, "🇨🇳") {
		t.Fatalf("emoji sequence lost: %q", whole.Text)
	}
	var perByte [][]byte
	for _, b := range []byte(input) {
		perByte = append(perByte, []byte{b})
	}
	chunked := run(perByte)
	if chunked.Text != whole.Text || chunked.CursorRow != whole.CursorRow || chunked.CursorCol != whole.CursorCol {
		t.Fatalf("chunked state diverged\nwhole:  %+v\nchunked: %+v", whole, chunked)
	}
	tabA := NewTab("a", "s", 30, 2, UTF8)
	t.Cleanup(tabA.Close)
	tabB := NewTab("b", "s", 30, 2, UTF8)
	t.Cleanup(tabB.Close)
	stream := "one\r\ntwo\r\né"
	tabA.Feed([]byte(stream))
	tabB.Feed([]byte("one\r\ntwo\r\ne"))
	tabB.Feed([]byte("́"))
	if tabA.ScreenText() != tabB.ScreenText() || tabA.ScrollbackLen() != tabB.ScrollbackLen() {
		t.Fatalf("mid-cluster split diverged\nA: %q (%d)\nB: %q (%d)",
			tabA.ScreenText(), tabA.ScrollbackLen(), tabB.ScreenText(), tabB.ScrollbackLen())
	}
	if !strings.Contains(tabB.ScreenText(), "é") {
		t.Fatalf("split combining mark lost: %q", tabB.ScreenText())
	}
}
