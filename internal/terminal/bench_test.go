package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

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

// BenchmarkFeedCombining scrolls lines of decomposed accents and edits them
// in place with ICH/DCH, the paths that keep combining marks per row.
func BenchmarkFeedCombining(b *testing.B) {
	line := strings.Repeat("éàö ñû ", 6)
	chunk := bytes.Repeat([]byte(line+"\r\x1b[3C\x1b[2@xy\x1b[5C\x1b[3P\r\n"), 512)
	for _, size := range [][2]int{{80, 24}, {200, 60}} {
		b.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(b *testing.B) {
			tab := NewTab("b", "s", size[0], size[1], UTF8)
			b.Cleanup(tab.Close)
			b.SetBytes(int64(len(chunk)))
			b.ReportAllocs()
			for b.Loop() {
				tab.Feed(chunk)
			}
		})
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
	if seconds, ok := nativeProcessCPUTime(); ok {
		return seconds, true
	}
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

const (
	p01Seed                = int64(20261015)
	p01WorkloadBytes       = 8_192_000
	p01RateLinesPerSecond  = 1000
	p01ReplayBytes         = 4 << 20
	p01StringHeaderBytes   = int(unsafe.Sizeof(""))
	p01HistoryProfileLines = ScrollbackLines
)

type p01Stage struct {
	Stage                  string  `json:"stage"`
	RSSBytes               int64   `json:"rss_bytes"`
	HeapInuseBytes         uint64  `json:"heap_inuse_bytes"`
	HeapAllocBytes         uint64  `json:"heap_alloc_bytes"`
	TotalAllocBytes        uint64  `json:"total_alloc_bytes"`
	NumGC                  uint32  `json:"num_gc"`
	PauseTotalNs           uint64  `json:"pause_total_ns"`
	HistorySlotBytes       uint64  `json:"history_slot_bytes"`
	HistorySlotHeapPercent float64 `json:"history_slot_heap_percent"`
	HistoryTextBytes       uint64  `json:"history_text_bytes"`
	HistoryStrings         int     `json:"history_strings"`
	HistoryLines           int     `json:"history_lines"`
	RingBytes              uint64  `json:"ring_bytes"`
}

type p01Result struct {
	Schema                  int        `json:"schema"`
	StartedAt               string     `json:"started_at"`
	GoVersion               string     `json:"go_version"`
	GOOS                    string     `json:"goos"`
	GOARCH                  string     `json:"goarch"`
	PID                     int        `json:"pid"`
	Seed                    int64      `json:"seed"`
	Scenario                string     `json:"scenario"`
	Tabs                    int        `json:"tabs"`
	Repeat                  int        `json:"repeat"`
	Cols                    int        `json:"cols"`
	Rows                    int        `json:"rows"`
	RateSeconds             int        `json:"rate_seconds"`
	RateTargetLinesPerSec   int        `json:"rate_target_lines_per_sec"`
	WorkloadBytesPerTab     int        `json:"workload_bytes_per_tab"`
	InputLinesPerTab        int64      `json:"input_lines_per_tab"`
	FeedBytesTotal          uint64     `json:"feed_bytes_total"`
	FeedElapsedNs           int64      `json:"feed_elapsed_ns"`
	FeedBusyNs              int64      `json:"feed_busy_ns"`
	FeedMiBPerSec           float64    `json:"feed_mib_per_sec"`
	ProcessingMiBPerSec     float64    `json:"processing_mib_per_sec"`
	LinesPerSecondPerTab    float64    `json:"lines_per_second_per_tab"`
	FeedNumGC               uint32     `json:"feed_num_gc"`
	FeedPauseTotalNs        uint64     `json:"feed_pause_total_ns"`
	AttachReplayBytes       int        `json:"attach_replay_bytes"`
	AttachP95Micros         int64      `json:"attach_p95_micros"`
	HistoryP95Micros        int64      `json:"history_p95_micros"`
	HistoryReplayLinesTotal int        `json:"history_replay_lines_total"`
	AttachMicros            []int64    `json:"attach_micros"`
	HistoryMicros           []int64    `json:"history_micros"`
	MemProfileRate          int        `json:"mem_profile_rate"`
	StringHeaderBytes       int        `json:"string_header_bytes"`
	ElapsedNs               int64      `json:"elapsed_ns"`
	Stages                  []p01Stage `json:"stages"`
}

func p01EnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("%s = %q: %v", name, value, err)
	}
	return n
}

func p01RSSBytes() (int64, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			return 0, err
		}
		fields := strings.Fields(string(out))
		if len(fields) != 1 {
			return 0, fmt.Errorf("unexpected ps output %q", out)
		}
		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		return kib * 1024, nil
	case "linux":
		data, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return 0, err
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected statm output %q", data)
		}
		pages, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return pages * int64(os.Getpagesize()), nil
	default:
		return 0, fmt.Errorf("RSS sampling unsupported on %s", runtime.GOOS)
	}
}

func p01WriteProfiles(t *testing.T, stage string) {
	t.Helper()
	dir := os.Getenv("NEXTERM_P01_PROFILE_DIR")
	if dir == "" || stage == "baseline" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, profileName := range []string{"allocs", "heap"} {
		name := fmt.Sprintf("%s-t%03d-r%02d-%s-%s.pb.gz",
			os.Getenv("NEXTERM_P01_SCENARIO"), p01EnvInt(t, "NEXTERM_P01_TABS", 50),
			p01EnvInt(t, "NEXTERM_P01_REPEAT", 0), stage, profileName)
		path := filepath.Join(dir, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		profile := pprof.Lookup(profileName)
		if profile == nil {
			_ = file.Close()
			t.Fatalf("profile %s not found", profileName)
		}
		if err := profile.WriteTo(file, 0); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func p01Capture(t *testing.T, name string, tabs []*Tab) p01Stage {
	t.Helper()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	rss, err := p01RSSBytes()
	if err != nil {
		t.Fatal(err)
	}
	stage := p01Stage{
		Stage:           name,
		RSSBytes:        rss,
		HeapInuseBytes:  stats.HeapInuse,
		HeapAllocBytes:  stats.HeapAlloc,
		TotalAllocBytes: stats.TotalAlloc,
		NumGC:           stats.NumGC,
		PauseTotalNs:    stats.PauseTotalNs,
	}
	for _, tab := range tabs {
		history := tab.scr.history
		stage.HistorySlotBytes += uint64(len(history.lines) * p01StringHeaderBytes)
		stage.HistoryLines += history.size
		stage.RingBytes += uint64(tab.ring.size)
		for _, line := range history.lines {
			if line == "" {
				continue
			}
			stage.HistoryStrings++
			stage.HistoryTextBytes += uint64(len(line))
		}
	}
	if stats.HeapAlloc > 0 {
		stage.HistorySlotHeapPercent = 100 * float64(stage.HistorySlotBytes) / float64(stats.HeapAlloc)
	}
	p01WriteProfiles(t, name)
	return stage
}

func p01ASCIILine(rng *rand.Rand, size int) []byte {
	line := make([]byte, size)
	for i := 0; i < size-2; i++ {
		line[i] = byte(33 + rng.Intn(94))
	}
	line[size-2], line[size-1] = '\r', '\n'
	return line
}

func p01ANSICJKLine(rng *rand.Rand) []byte {
	var line strings.Builder
	fmt.Fprintf(&line, "\x1b[38;5;%dm", 16+rng.Intn(216))
	cjk := []rune("终端滚动历史内存基线中文输出测试")
	for i := 0; i < 20; i++ {
		line.WriteRune(cjk[rng.Intn(len(cjk))])
	}
	line.WriteString(strings.Repeat("x", 40))
	const suffix = "\x1b[0m\r\n"
	target := 128 - len(suffix)
	for target-line.Len() >= 4 {
		line.WriteString("\x1b[0m")
	}
	if remainder := target - line.Len(); remainder > 0 {
		line.WriteString(strings.Repeat("x", remainder))
	}
	line.WriteString(suffix)
	return []byte(line.String())
}

func p01Workload(t *testing.T, scenario string) ([]byte, int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(p01Seed))
	switch scenario {
	case "idle":
		return nil, 0
	case "rate80":
		return bytes.Repeat(p01ASCIILine(rng, 80), p01RateLinesPerSecond), p01RateLinesPerSecond
	case "line80":
		return bytes.Repeat(p01ASCIILine(rng, 80), p01WorkloadBytes/80), p01WorkloadBytes / 80
	case "long8k":
		return bytes.Repeat(p01ASCIILine(rng, 8192), p01WorkloadBytes/8192), p01WorkloadBytes / 8192
	case "ansicjk":
		line := p01ANSICJKLine(rng)
		if len(line) != 128 {
			t.Fatalf("ANSI/CJK line = %d bytes, want 128", len(line))
		}
		return bytes.Repeat(line, p01WorkloadBytes/len(line)), int64(p01WorkloadBytes / len(line))
	default:
		t.Fatalf("unknown P01 scenario %q", scenario)
		return nil, 0
	}
}

func p01Warmup(t *testing.T, payload []byte) {
	t.Helper()
	tabs := []*Tab{
		NewTab("p01-warm-0", "p01-warm", 120, 40, UTF8),
		NewTab("p01-warm-1", "p01-warm", 120, 40, UTF8),
	}
	limit := min(len(payload), 256*1024)
	for i, tab := range tabs {
		for offset := 0; offset < limit; offset += 64 * 1024 {
			tab.Feed(payload[offset:min(offset+64*1024, limit)])
		}
		lines := tab.ScreenHistory(p01HistoryProfileLines)
		runtime.KeepAlive(lines)
		output := NewOutput(tab)
		sink := SinkFunc(func(context.Context, []byte) error { return nil })
		if err := output.AttachReplay(context.Background(), fmt.Sprintf("warm-%d", i), sink, p01ReplayBytes); err != nil {
			output.Close()
			t.Fatal(err)
		}
		output.Close()
		tab.Close()
	}
	runtime.KeepAlive(tabs)
}

func p01Feed(tabs []*Tab, scenario string, payload []byte, rateSeconds int) (busy, elapsed time.Duration) {
	if scenario == "idle" {
		return 0, 0
	}
	start := time.Now()
	if scenario == "rate80" {
		next := start
		for tick := 0; tick < rateSeconds; tick++ {
			for _, tab := range tabs {
				feedStart := time.Now()
				tab.Feed(payload)
				busy += time.Since(feedStart)
			}
			next = next.Add(time.Second)
			if delay := time.Until(next); delay > 0 {
				time.Sleep(delay)
			}
		}
		return busy, time.Since(start)
	}
	for _, tab := range tabs {
		for offset := 0; offset < len(payload); offset += 64 * 1024 {
			feedStart := time.Now()
			tab.Feed(payload[offset:min(offset+64*1024, len(payload))])
			busy += time.Since(feedStart)
		}
	}
	return busy, time.Since(start)
}

func p01MeasureReplays(t *testing.T, tabs []*Tab) (attach, history []int64, historyLines int) {
	t.Helper()
	sink := SinkFunc(func(context.Context, []byte) error { return nil })
	for i, tab := range tabs {
		start := time.Now()
		lines := tab.ScreenHistory(p01HistoryProfileLines)
		history = append(history, time.Since(start).Microseconds())
		historyLines += len(lines)
		runtime.KeepAlive(lines)

		output := NewOutput(tab)
		start = time.Now()
		err := output.AttachReplay(context.Background(), fmt.Sprintf("first-%d", i), sink, p01ReplayBytes)
		attach = append(attach, time.Since(start).Microseconds())
		output.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return attach, history, historyLines
}

func p01CloseTabs(tabs []*Tab) {
	for i, tab := range tabs {
		tab.Close()
		tabs[i] = nil
	}
}

func p01P95(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	ordered := append([]int64(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[(95*len(ordered)+99)/100-1]
}

func p01WriteResult(t *testing.T, result p01Result) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := os.Getenv("NEXTERM_P01_OUTPUT")
	if path == "" {
		fmt.Printf("P01_JSON %s", data)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestP01ScrollbackBaseline(t *testing.T) {
	if os.Getenv("NEXTERM_P01_BASELINE") != "1" {
		t.Skip("P01 baseline requires NEXTERM_P01_BASELINE=1")
	}
	started := time.Now()
	scenario := os.Getenv("NEXTERM_P01_SCENARIO")
	if scenario == "" {
		scenario = "idle"
	}
	tabCount := p01EnvInt(t, "NEXTERM_P01_TABS", 50)
	switch tabCount {
	case 1, 10, 50, 100:
	default:
		t.Fatalf("P01 tab count = %d, want 1, 10, 50, or 100", tabCount)
	}
	repeat := p01EnvInt(t, "NEXTERM_P01_REPEAT", 0)
	rateSeconds := p01EnvInt(t, "NEXTERM_P01_RATE_SECONDS", 10)
	if rateSeconds <= 0 {
		t.Fatalf("P01 rate seconds = %d", rateSeconds)
	}
	payload, payloadLines := p01Workload(t, scenario)
	p01Warmup(t, payload)
	baseline := p01Capture(t, "baseline", nil)

	tabs := make([]*Tab, tabCount)
	for i := range tabs {
		tabs[i] = NewTab(fmt.Sprintf("p01-%d", i), "p01", 120, 40, UTF8)
	}
	idle := p01Capture(t, "idle", tabs)
	workloadBytes := len(payload)
	inputLines := payloadLines
	if scenario == "rate80" {
		workloadBytes *= rateSeconds
		inputLines *= int64(rateSeconds)
	}
	var beforeFeed, afterFeed runtime.MemStats
	runtime.ReadMemStats(&beforeFeed)
	busy, feedElapsed := p01Feed(tabs, scenario, payload, rateSeconds)
	runtime.ReadMemStats(&afterFeed)
	loaded := p01Capture(t, "loaded", tabs)
	attach, history, historyLines := p01MeasureReplays(t, tabs)
	for _, tab := range tabs {
		tab.Feed([]byte("\x1b[3J"))
	}
	cleared := p01Capture(t, "cleared", tabs)
	p01CloseTabs(tabs)
	tabs = nil
	payload = nil
	closed := p01Capture(t, "closed", nil)

	result := p01Result{
		Schema:                  1,
		StartedAt:               started.UTC().Format(time.RFC3339Nano),
		GoVersion:               runtime.Version(),
		GOOS:                    runtime.GOOS,
		GOARCH:                  runtime.GOARCH,
		PID:                     os.Getpid(),
		Seed:                    p01Seed,
		Scenario:                scenario,
		Tabs:                    tabCount,
		Repeat:                  repeat,
		Cols:                    120,
		Rows:                    40,
		RateSeconds:             rateSeconds,
		RateTargetLinesPerSec:   p01RateLinesPerSecond,
		WorkloadBytesPerTab:     workloadBytes,
		InputLinesPerTab:        inputLines,
		FeedBytesTotal:          uint64(workloadBytes * tabCount),
		FeedElapsedNs:           feedElapsed.Nanoseconds(),
		FeedBusyNs:              busy.Nanoseconds(),
		FeedNumGC:               afterFeed.NumGC - beforeFeed.NumGC,
		FeedPauseTotalNs:        afterFeed.PauseTotalNs - beforeFeed.PauseTotalNs,
		AttachReplayBytes:       p01ReplayBytes,
		AttachP95Micros:         p01P95(attach),
		HistoryP95Micros:        p01P95(history),
		HistoryReplayLinesTotal: historyLines,
		AttachMicros:            attach,
		HistoryMicros:           history,
		MemProfileRate:          runtime.MemProfileRate,
		StringHeaderBytes:       p01StringHeaderBytes,
		ElapsedNs:               time.Since(started).Nanoseconds(),
		Stages:                  []p01Stage{baseline, idle, loaded, cleared, closed},
	}
	if feedElapsed > 0 {
		result.FeedMiBPerSec = float64(result.FeedBytesTotal) / feedElapsed.Seconds() / 1024 / 1024
		result.LinesPerSecondPerTab = float64(inputLines) / feedElapsed.Seconds()
	}
	if busy > 0 {
		result.ProcessingMiBPerSec = float64(result.FeedBytesTotal) / busy.Seconds() / 1024 / 1024
	}
	p01WriteResult(t, result)
	t.Logf("P01 %s tabs=%d repeat=%d elapsed=%v RSS loaded=%d cleared=%d closed=%d",
		scenario, tabCount, repeat, time.Since(started), loaded.RSSBytes, cleared.RSSBytes, closed.RSSBytes)
}

type p01RefillCycle struct {
	Cycle             int     `json:"cycle"`
	InputBytes        int     `json:"input_bytes"`
	ElapsedNs         int64   `json:"elapsed_ns"`
	ClearElapsedNs    int64   `json:"clear_elapsed_ns"`
	RefillElapsedNs   int64   `json:"refill_elapsed_ns"`
	MiBPerSec         float64 `json:"mib_per_sec"`
	AllocBytes        uint64  `json:"alloc_bytes"`
	Mallocs           uint64  `json:"mallocs"`
	NumGC             uint32  `json:"num_gc"`
	PauseTotalNs      uint64  `json:"pause_total_ns"`
	HeapAllocBytes    uint64  `json:"heap_alloc_bytes"`
	HeapInuseBytes    uint64  `json:"heap_inuse_bytes"`
	ClearBackingLines int     `json:"clear_backing_lines"`
	BackingLines      int     `json:"backing_lines"`
	BackingGrowths    int     `json:"backing_growths"`
	HistoryLines      int     `json:"history_lines"`
	RingBytes         int     `json:"ring_bytes"`
}

type p01RefillResult struct {
	Schema              int              `json:"schema"`
	StartedAt           string           `json:"started_at"`
	GoVersion           string           `json:"go_version"`
	GOOS                string           `json:"goos"`
	GOARCH              string           `json:"goarch"`
	PID                 int              `json:"pid"`
	Seed                int64            `json:"seed"`
	Capacity            int              `json:"capacity"`
	Cycles              int              `json:"cycles"`
	Repeat              int              `json:"repeat"`
	Cols                int              `json:"cols"`
	Rows                int              `json:"rows"`
	LineBytes           int              `json:"line_bytes"`
	WarmupCycles        int              `json:"warmup_cycles"`
	InitialHistoryLines int              `json:"initial_history_lines"`
	FinalDumpMatches    bool             `json:"final_dump_matches"`
	CycleResults        []p01RefillCycle `json:"cycle_results"`
}

func p01RefillGrowths(initial, final int) int {
	growths := 0
	for initial < final {
		initial = min(initial*2, final)
		growths++
	}
	return growths
}

func p01RefillFeed(tab *Tab, payload []byte) {
	for offset := 0; offset < len(payload); offset += 64 * 1024 {
		tab.Feed(payload[offset:min(offset+64*1024, len(payload))])
	}
}

func p01WriteRefillResult(t *testing.T, result p01RefillResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := os.Getenv("NEXTERM_P01_REFILL_OUTPUT")
	if path == "" {
		fmt.Printf("P01_REFILL_JSON %s", data)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestP01ClearRefillBenchmark(t *testing.T) {
	if os.Getenv("NEXTERM_P01_REFILL") != "1" {
		t.Skip("P01 clear/refill benchmark requires NEXTERM_P01_REFILL=1")
	}
	started := time.Now()
	capacity := p01EnvInt(t, "NEXTERM_P01_REFILL_CAPACITY", 4096)
	switch capacity {
	case 4096, ScrollbackLines:
	default:
		t.Fatalf("P01 refill capacity = %d, want 4096 or %d", capacity, ScrollbackLines)
	}
	cycles := p01EnvInt(t, "NEXTERM_P01_REFILL_CYCLES", 0)
	if cycles == 0 {
		if capacity == ScrollbackLines {
			cycles = 5
		} else {
			cycles = 20
		}
	}
	if cycles < 2 {
		t.Fatalf("P01 refill cycles = %d, want at least 2", cycles)
	}
	repeat := p01EnvInt(t, "NEXTERM_P01_REPEAT", 0)
	rng := rand.New(rand.NewSource(p01Seed))
	line := p01ASCIILine(rng, 80)
	payload := bytes.Repeat(line, capacity)
	initial := make([]byte, 0, len(payload)+39*len(line))
	initial = append(initial, payload...)
	initial = append(initial, bytes.Repeat(line, 39)...)
	const clear = "\x1b[3J"

	warm := NewTab("p01-refill-warm", "p01-refill", 120, 40, UTF8, WithScreenHistory(capacity))
	p01RefillFeed(warm, initial)
	for i := 0; i < 2; i++ {
		warm.Feed([]byte(clear))
		p01RefillFeed(warm, payload)
	}
	warm.Close()
	warm = nil

	tab := NewTab("p01-refill", "p01-refill", 120, 40, UTF8, WithScreenHistory(capacity))
	t.Cleanup(tab.Close)
	p01RefillFeed(tab, initial)
	if got := tab.ScrollbackLen(); got != capacity {
		t.Fatalf("initial history = %d, want %d", got, capacity)
	}
	runtime.GC()
	cycleResults := make([]p01RefillCycle, cycles)
	for cycle := 0; cycle < cycles; cycle++ {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		tab.Feed([]byte(clear))
		clearElapsed := time.Since(start)
		if got := tab.ScrollbackLen(); got != 0 {
			t.Fatalf("history after ED3 = %d", got)
		}
		clearBacking := len(tab.scr.history.lines)
		refillStart := time.Now()
		p01RefillFeed(tab, payload)
		refillElapsed := time.Since(refillStart)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		if got := tab.ScrollbackLen(); got != capacity {
			t.Fatalf("history after refill = %d, want %d", got, capacity)
		}
		backing := len(tab.scr.history.lines)
		inputBytes := len(payload) + len(clear)
		cycleResults[cycle] = p01RefillCycle{
			Cycle:             cycle,
			InputBytes:        inputBytes,
			ElapsedNs:         elapsed.Nanoseconds(),
			ClearElapsedNs:    clearElapsed.Nanoseconds(),
			RefillElapsedNs:   refillElapsed.Nanoseconds(),
			MiBPerSec:         float64(inputBytes) / elapsed.Seconds() / 1024 / 1024,
			AllocBytes:        after.TotalAlloc - before.TotalAlloc,
			Mallocs:           after.Mallocs - before.Mallocs,
			NumGC:             after.NumGC - before.NumGC,
			PauseTotalNs:      after.PauseTotalNs - before.PauseTotalNs,
			HeapAllocBytes:    after.HeapAlloc,
			HeapInuseBytes:    after.HeapInuse,
			ClearBackingLines: clearBacking,
			BackingLines:      backing,
			BackingGrowths:    p01RefillGrowths(clearBacking, backing),
			HistoryLines:      tab.ScrollbackLen(),
			RingBytes:         tab.ring.Len(),
		}
	}
	if got := len(tab.ScreenHistory(capacity)); got != capacity {
		t.Fatalf("final ScreenHistory length = %d, want %d", got, capacity)
	}
	finalDumpMatches := bytes.Equal(tab.Dump(len(payload)), payload)
	if !finalDumpMatches {
		t.Fatal("final raw export does not match the last refill")
	}
	p01WriteRefillResult(t, p01RefillResult{
		Schema:              1,
		StartedAt:           started.UTC().Format(time.RFC3339Nano),
		GoVersion:           runtime.Version(),
		GOOS:                runtime.GOOS,
		GOARCH:              runtime.GOARCH,
		PID:                 os.Getpid(),
		Seed:                p01Seed,
		Capacity:            capacity,
		Cycles:              cycles,
		Repeat:              repeat,
		Cols:                120,
		Rows:                40,
		LineBytes:           len(line),
		WarmupCycles:        2,
		InitialHistoryLines: capacity,
		FinalDumpMatches:    finalDumpMatches,
		CycleResults:        cycleResults,
	})
	t.Logf("P01 refill capacity=%d cycles=%d repeat=%d elapsed=%v", capacity, cycles, repeat, time.Since(started))
}

// BenchmarkFeedShortLines is the scroll-heavy regression baseline (`yes`): every line scrolls a row into history.
func BenchmarkFeedShortLines(b *testing.B) {
	tab := NewTab("b", "s", 120, 40, UTF8)
	b.Cleanup(tab.Close)
	chunk := bytes.Repeat([]byte("y\r\n"), 16*1024)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tab.Feed(chunk)
	}
}

// BenchmarkFeedColoredLog feeds a colored build log, dominated by SGR parsing plus scrolling.
func BenchmarkFeedColoredLog(b *testing.B) {
	tab := NewTab("b", "s", 120, 40, UTF8)
	b.Cleanup(tab.Close)
	var buf bytes.Buffer
	for i := 0; buf.Len() < 64*1024; i++ {
		fmt.Fprintf(&buf, "\x1b[32m[%05d/%05d]\x1b[0m CC internal/pkg%d/file_%d.o \x1b[90m(%d.%03ds)\x1b[0m\r\n", i%9999, 9999, i%37, i, i%7, (i*131)%1000)
	}
	chunk := buf.Bytes()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tab.Feed(chunk)
	}
}
