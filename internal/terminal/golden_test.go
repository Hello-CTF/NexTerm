package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

var updateGolden = flag.Bool("update", false, "regenerate golden fixtures")

type goldenOp struct {
	feed   string
	resize *[2]int
}

func feedOp(s string) goldenOp { return goldenOp{feed: s} }

func resizeOp(cols, rows int) goldenOp { return goldenOp{resize: &[2]int{cols, rows}} }

type goldenScenario struct {
	cols, rows int
	enc        Encoding
	ops        []goldenOp
}

type goldenResult struct {
	Snapshot      Snapshot `json:"snapshot"`
	State         State    `json:"state"`
	ScrollbackLen int      `json:"scrollbackLen"`
	History       []string `json:"history"`
	Tail          []string `json:"tail"`
	DumpSHA256    string   `json:"dumpSha256"`
}

func encodeScenario(t *testing.T, enc interface {
	Bytes([]byte) ([]byte, error)
}, s string) string {
	t.Helper()
	raw, err := enc.Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func goldenScenarios(t *testing.T) map[string]goldenScenario {
	t.Helper()
	gbk := func(s string) string {
		return encodeScenario(t, simplifiedchinese.GBK.NewEncoder(), s)
	}
	big5 := func(s string) string {
		return encodeScenario(t, traditionalchinese.Big5.NewEncoder(), s)
	}
	latin1 := func(s string) string {
		return encodeScenario(t, charmap.Windows1252.NewEncoder(), s)
	}
	numbered := ""
	for i := 0; i < 60; i++ {
		numbered += fmt.Sprintf("row-%02d abcdef\r\n", i)
	}
	return map[string]goldenScenario{
		"shell-colors": {cols: 80, rows: 24, enc: UTF8, ops: []goldenOp{
			feedOp("\x1b[2J\x1b[H"),
			feedOp("user@host:~$ \x1b[31;1mred-bold\x1b[0m plain \x1b[38;5;208morange\x1b[0m \x1b[38;2;1;2;3mrgb\x1b[0m\r\n"),
			feedOp("line2\x1b[3DXY\x1b[2K\r\n"),
			feedOp("user@host:~$ "),
		}},
		"vim-altscreen": {cols: 80, rows: 10, enc: UTF8, ops: []goldenOp{
			feedOp("before-vim\r\n"),
			feedOp("\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"),
			feedOp("~ vim line1\r\n~ vim line2"),
			feedOp("\x1b[?25h\x1b[?1049l"),
			feedOp("after-vim"),
		}},
		"cjk-wrap": {cols: 20, rows: 6, enc: UTF8, ops: []goldenOp{
			feedOp("中文宽度测试 wrapping 行为\r\n"),
			feedOp("emoji 🚀🙂 test ascii\r\n"),
			feedOp("tail"),
		}},
		"grapheme-clusters": {cols: 30, rows: 6, enc: UTF8, ops: []goldenOp{
			feedOp("é combining\r\n"),
			feedOp("family 👨‍👩‍👧 zwj flag 🇨🇳\r\n"),
			feedOp("end"),
		}},
		"scroll-history": {cols: 40, rows: 6, enc: UTF8, ops: []goldenOp{
			feedOp(numbered),
		}},
		"ed3-history": {cols: 40, rows: 5, enc: UTF8, ops: []goldenOp{
			feedOp(numbered),
			feedOp("\x1b[3J"),
			feedOp("after-erase-1\r\nafter-erase-2\r\nafter-erase-3\r\n"),
		}},
		"resize-reflow": {cols: 30, rows: 6, enc: UTF8, ops: []goldenOp{
			feedOp("a very long line that will wrap around the thirty column grid\r\n"),
			feedOp("second line\r\n"),
			resizeOp(20, 4),
			feedOp("after-resize\r\n"),
			resizeOp(30, 6),
			feedOp("end"),
		}},
		"gbk-session": {cols: 80, rows: 24, enc: GBK, ops: []goldenOp{
			feedOp(gbk("C:\\> 中文目录列表\r\n")),
			feedOp(gbk("服务器编码 与 ASCII mixed\r\n")),
			feedOp("prompt> "),
		}},
		"big5-session": {cols: 80, rows: 24, enc: Big5, ops: []goldenOp{
			feedOp(big5("繁體中文測試 第一行\r\n")),
			feedOp(big5("第二行 結束\r\n")),
		}},
		"latin1-session": {cols: 80, rows: 24, enc: Latin1, ops: []goldenOp{
			feedOp(latin1("café €99 hotel\r\n")),
			feedOp(string([]byte{'a', 0x81, 'b', 0xFF, '\r', '\n'})),
		}},
		"modes-bracketed": {cols: 80, rows: 24, enc: UTF8, ops: []goldenOp{
			feedOp("\x1b[?2004h"),
			feedOp("paste-on\r\n"),
			feedOp("\x1b[?25l\x1b[?1h"),
			feedOp("hidden-cursor\r\n"),
			feedOp("\x1b[?2004l\x1b[?25h"),
			feedOp("done"),
		}},
		"control-ops": {cols: 40, rows: 8, enc: UTF8, ops: []goldenOp{
			feedOp("a\tb\tc\r\n"),
			feedOp("backspace-xy\b\bZ\r\n"),
			feedOp("cr-overwrite\rNEW\r\n"),
			feedOp("\x1b[2;5r"),
			feedOp("\x1b[5;1Hregion-bottom\r\n"),
			feedOp("\x1b[2L\x1b[1M\x1b[r"),
			feedOp("\x1b]0;title[?2004h\x07"),
			feedOp("osc-done\x1b[3D\x1b[P!\r\n"),
			feedOp("end"),
		}},
		"wide-edits": {cols: 20, rows: 4, enc: UTF8, ops: []goldenOp{
			feedOp("中\r"),
			feedOp("x"),
			feedOp("\x1b[3C"),
			feedOp("Y\r\n"),
			feedOp("a中b\x1b[2G\x1b[P\r\n"),
			feedOp("x中y\x1b[2G\x1b[X\r\n"),
			feedOp("q中z\x1b[3G\x1b[@"),
		}},
	}
}

func runGoldenScenario(t *testing.T, sc goldenScenario, chunkFor func(op int, data []byte) [][]byte) goldenResult {
	t.Helper()
	clock := newFakeClock()
	tab := NewTab("golden", "s", sc.cols, sc.rows, sc.enc, WithClock(clock.now))
	t.Cleanup(tab.Close)
	for i, op := range sc.ops {
		if op.resize != nil {
			if err := tab.Resize(op.resize[0], op.resize[1]); err != nil {
				t.Fatal(err)
			}
			continue
		}
		for _, chunk := range chunkFor(i, []byte(op.feed)) {
			tab.Feed(chunk)
		}
	}
	snap := tab.Snapshot()
	sum := sha256.Sum256(tab.Dump(0))
	return goldenResult{
		Snapshot:      snap,
		State:         tab.State(),
		ScrollbackLen: tab.ScrollbackLen(),
		History:       tab.ScreenHistory(5),
		Tail:          tab.TailLines(8),
		DumpSHA256:    hex.EncodeToString(sum[:]),
	}
}

func wholeChunks(_ int, data []byte) [][]byte { return [][]byte{data} }

func fixedChunks(n int) func(int, []byte) [][]byte {
	return func(_ int, data []byte) [][]byte {
		var out [][]byte
		for len(data) > 0 {
			k := min(n, len(data))
			out = append(out, data[:k])
			data = data[k:]
		}
		return out
	}
}

func randomChunks(seed int64) func(int, []byte) [][]byte {
	return func(op int, data []byte) [][]byte {
		rng := rand.New(rand.NewSource(seed + int64(op)))
		var out [][]byte
		for len(data) > 0 {
			k := min(1+rng.Intn(64), len(data))
			out = append(out, data[:k])
			data = data[k:]
		}
		return out
	}
}

func TestGoldenScreens(t *testing.T) {
	scenarios := goldenScenarios(t)
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			got := runGoldenScenario(t, sc, wholeChunks)
			gotJSON, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "golden", name+".json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(gotJSON, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if string(gotJSON)+"\n" != string(want) {
				t.Errorf("golden mismatch for %s (review and rerun with -update if intentional)\ngot:\n%s", name, gotJSON)
			}

			for label, chunkFor := range map[string]func(int, []byte) [][]byte{
				"1byte":  fixedChunks(1),
				"3byte":  fixedChunks(3),
				"7byte":  fixedChunks(7),
				"random": randomChunks(42),
			} {
				other := runGoldenScenario(t, sc, chunkFor)
				otherJSON, err := json.Marshal(other)
				if err != nil {
					t.Fatal(err)
				}
				baseJSON, _ := json.Marshal(got)
				if string(otherJSON) != string(baseJSON) {
					t.Errorf("chunking %s changed the result\nwhole:\n%s\nchunked:\n%s", label, baseJSON, otherJSON)
				}
			}
		})
	}
}
