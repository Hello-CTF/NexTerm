package terminal

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var benchChunk = bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog 0123456789 \r\n"), 2048)

func BenchmarkFeedASCII(b *testing.B) {
	tab := NewTab("b", "s", 80, 24, UTF8)
	b.Cleanup(tab.Close)
	chunk := benchChunk[:64*1024]
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tab.Feed(chunk)
	}
}

func BenchmarkFeedCJK(b *testing.B) {
	tab := NewTab("b", "s", 80, 24, UTF8)
	b.Cleanup(tab.Close)
	chunk := bytes.Repeat([]byte("中文输出测试，终端性能基准。\r\n"), 2048)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tab.Feed(chunk)
	}
}

func BenchmarkFeedRandomBinary(b *testing.B) {
	tab := NewTab("b", "s", 80, 24, UTF8)
	b.Cleanup(tab.Close)
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = byte(i%251) + 1
	}
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tab.Feed(chunk)
	}
}

func BenchmarkTranscodeGBK(b *testing.B) {
	raw, err := simplifiedchinese.GBK.NewEncoder().Bytes(bytes.Repeat([]byte("服务器编码转换性能测试。"), 4096))
	if err != nil {
		b.Fatal(err)
	}
	tr := NewTranscoder(GBK)
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tr.Feed(raw)
	}
}

func BenchmarkRingPush(b *testing.B) {
	r := NewRing(ScrollbackBytes)
	chunk := make([]byte, 64*1024)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Push(chunk)
	}
}

func BenchmarkOutputVisibleFanout(b *testing.B) {
	tab := NewTab("b", "s", 80, 24, UTF8)
	b.Cleanup(tab.Close)
	o := NewOutput(tab)
	b.Cleanup(o.Close)
	discard := SinkFunc(func(context.Context, []byte) error { return nil })
	o.Attach("a", discard)
	o.Attach("b", discard)
	chunk := benchChunk[:64*1024]
	ctx := context.Background()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := o.Chunk(ctx, chunk); err != nil {
			b.Fatal(err)
		}
	}
}

func buildAcceptanceWorkload(t *testing.T, enc Encoding) (chunks [][]byte, total int) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	for total < 100*1024*1024 {
		var text strings.Builder
		for text.Len() < 64*1024 {
			n := 20 + rng.Intn(60)
			for i := 0; i < n; i++ {
				if enc == GBK && i%7 == 0 {
					text.WriteRune(rune(0x4e00 + rng.Intn(200)))
				} else {
					text.WriteByte(byte(32 + rng.Intn(95)))
				}
			}
			text.WriteString("\r\n")
		}
		chunk := []byte(text.String())
		if enc == GBK {
			chunk = encodeForTest(t, simplifiedchinese.GBK, text.String())
		}
		chunks = append(chunks, chunk)
		total += len(chunk)
	}
	return chunks, total
}

const linuxUserHZ = 100

func processCPUTime() (float64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	paren := bytes.LastIndexByte(data, ')')
	if paren < 0 || paren+2 >= len(data) {
		return 0, false
	}
	fields := strings.Fields(string(data[paren+2:]))
	if len(fields) < 13 {
		return 0, false
	}
	utime, errU := strconv.ParseInt(fields[11], 10, 64)
	stime, errS := strconv.ParseInt(fields[12], 10, 64)
	if errU != nil || errS != nil {
		return 0, false
	}
	return float64(utime+stime) / linuxUserHZ, true
}

func TestThroughputAcceptanceWorkload(t *testing.T) {
	if testing.Short() || raceDetectorEnabled {
		t.Skip("throughput test skipped in -short and -race")
	}
	for _, enc := range []Encoding{UTF8, GBK} {
		t.Run(enc.String(), func(t *testing.T) {
			chunks, total := buildAcceptanceWorkload(t, enc)
			tab := NewTab("bench", "s", 120, 40, enc)
			t.Cleanup(tab.Close)
			tab.SetVisible(false)
			cpuBefore, haveCPUTime := processCPUTime()
			start := time.Now()
			for i, chunk := range chunks {
				if i%16 == 0 {
					tab.Feed([]byte("\x1b[2J\x1b[H"))
				}
				tab.Feed(chunk)
			}
			elapsed := time.Since(start)
			tab.Feed([]byte("final-marker"))
			wallMBps := float64(total) / elapsed.Seconds() / 1024 / 1024
			rate, metric := wallMBps, "wall"
			if haveCPUTime {
				if cpuAfter, ok := processCPUTime(); ok && cpuAfter > cpuBefore {
					rate = float64(total) / (cpuAfter - cpuBefore) / 1024 / 1024
					metric = "cpu"
				}
			}
			t.Logf("%s: %d MiB in %v => %.1f MiB/s %s (wall %.1f, %.1f MB/s); Rust refs 43.4/39.0 and 149.3/149.7 MB/s",
				enc, total/1024/1024, elapsed, rate, metric, wallMBps, float64(total)/elapsed.Seconds()/1e6)
			if rate < 10 {
				t.Fatalf("%s %s throughput %.1f MiB/s below the >=10 MB/s acceptance budget", enc, metric, rate)
			}
			if got := len(tab.Dump(0)); got != ScrollbackBytes {
				t.Fatalf("retained = %d, want exactly %d (32 MiB cap)", got, ScrollbackBytes)
			}
			snap := tab.Snapshot()
			if snap.Cols != 120 || snap.Rows != 40 || len(snap.Lines) != 40 {
				t.Fatalf("screen = %dx%d lines=%d", snap.Cols, snap.Rows, len(snap.Lines))
			}
			if strings.Contains(snap.Text, "\x1b[") {
				t.Fatal("ANSI residue on screen")
			}
			if !strings.Contains(snap.Text, "final-marker") {
				t.Fatal("final marker missing")
			}
		})
	}
}

func ExampleEncodeSend() {
	fmt.Printf("%q\n", EncodeSend("ls<enter>", false))
	// Output: "ls\r"
}
