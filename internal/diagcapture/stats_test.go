package diagcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEventAggregatorWindowsAndCaps(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "e.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	clock := time.Unix(1000, 0)
	agg := newEventAggregator(file, func() time.Time { return clock })
	agg.flush() // empty window writes nothing
	agg.record("a", 10)
	agg.record("a", 5)
	agg.record("b", -3)
	for i := 0; i < maxTopicsPerWindow+10; i++ {
		agg.record(fmt.Sprintf("t%d", i), 1)
	}
	clock = clock.Add(5 * time.Second)
	agg.flush()
	agg.record("a", 1)
	clock = clock.Add(5 * time.Second)
	agg.flush()

	lines := readJSONLines(t, file.Name())
	if len(lines) != 2 {
		t.Fatalf("want 2 windows, got %d", len(lines))
	}
	topics := lines[0]["topics"].(map[string]any)
	if len(topics) != maxTopicsPerWindow {
		t.Fatalf("topics not capped: %d", len(topics))
	}
	a := topics["a"].(map[string]any)
	if a["count"].(float64) != 2 || a["bytes"].(float64) != 15 {
		t.Fatalf("a = %v", a)
	}
	// 2 named topics plus 62 of the generated ones fit; the remaining 12 generated topics fold into other.
	if other := lines[0]["other"].(map[string]any); other["count"].(float64) != 12 {
		t.Fatalf("other = %v", other)
	}
	if lines[0]["windowSec"].(float64) != 5 {
		t.Fatalf("windowSec = %v", lines[0]["windowSec"])
	}
	if _, ok := lines[1]["topics"].(map[string]any)["a"]; !ok || lines[1]["other"] != nil {
		t.Fatalf("second window = %v", lines[1])
	}
}

func TestEventAggregatorFileCap(t *testing.T) {
	file, _ := os.Create(filepath.Join(t.TempDir(), "e.jsonl"))
	defer file.Close()
	agg := newEventAggregator(file, time.Now)
	agg.written = maxEventStatsBytes - 5
	agg.record("a", 1)
	agg.flush()
	if info, _ := file.Stat(); info.Size() != 0 {
		t.Fatalf("write beyond cap: %d", info.Size())
	}
}

func TestRecordEventInactiveAndConcurrent(t *testing.T) {
	RecordEvent("x", 1) // no capture: must be a no-op
	file, _ := os.Create(filepath.Join(t.TempDir(), "e.jsonl"))
	defer file.Close()
	agg := newEventAggregator(file, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				agg.record("x", 2)
				if j%25 == 0 {
					agg.flush()
				}
			}
		}()
	}
	wg.Wait()
	agg.flush()
	var count float64
	for _, line := range readJSONLines(t, file.Name()) {
		count += line["topics"].(map[string]any)["x"].(map[string]any)["count"].(float64)
	}
	if count != 800 {
		t.Fatalf("lost events: %v", count)
	}
}

func TestPayloadSize(t *testing.T) {
	if PayloadSize([]byte("abc")) != 3 || PayloadSize("ab") != 2 || PayloadSize(struct{}{}) != 0 || PayloadSize(nil) != 0 {
		t.Fatal("unexpected payload sizes")
	}
}

func TestIPCSummaryPercentilesOrderingAndBounds(t *testing.T) {
	agg := newIPCAggregator()
	at := time.Unix(1, 0)
	for i := 1; i <= 100; i++ {
		agg.add(ipcCall{cmd: "fast", elapsedMs: int64(i), ok: i%10 != 0, argsBytes: 2, resultLen: 3, at: at})
	}
	agg.add(ipcCall{cmd: "slow", elapsedMs: 6000, ok: true, resultLen: 7, at: at})
	agg.add(ipcCall{cmd: "unknownTime", elapsedMs: -1, ok: true, at: at})
	for i := 0; i < maxIPCCommands+50; i++ {
		agg.add(ipcCall{cmd: fmt.Sprintf("c%d", i), elapsedMs: 1, ok: true, at: at})
	}
	sum := agg.summary()
	if len(sum.Commands) > maxIPCCommands+1 {
		t.Fatalf("commands unbounded: %d", len(sum.Commands))
	}
	if sum.Commands[0].Cmd != "slow" || sum.Commands[1].Cmd != "fast" {
		t.Fatalf("not sorted by total ms: %v %v", sum.Commands[0].Cmd, sum.Commands[1].Cmd)
	}
	var fast ipcCmdSummary
	for _, c := range sum.Commands {
		if c.Cmd == "fast" {
			fast = c
		}
	}
	if fast.Count != 100 || fast.Errors != 10 || fast.TotalMs != 5050 || fast.P50Ms != 50 || fast.P95Ms != 95 || fast.MaxMs != 100 || fast.ArgsBytes != 200 || fast.ResultBytes != 300 {
		t.Fatalf("fast = %+v", fast)
	}
	if len(sum.Slowest) != ipcSlowestKept || sum.Slowest[0].Cmd != "slow" || sum.Slowest[0].ElapsedMs != 6000 || sum.Slowest[1].ElapsedMs != 100 {
		t.Fatalf("slowest = %+v", sum.Slowest[:2])
	}
	for i := 1; i < len(sum.Slowest); i++ {
		if sum.Slowest[i].ElapsedMs > sum.Slowest[i-1].ElapsedMs {
			t.Fatal("slowest not descending")
		}
	}
	if percentile(nil, 50) != 0 {
		t.Fatal("empty percentile")
	}
}

func TestIPCReservoirBounded(t *testing.T) {
	agg := newIPCAggregator()
	for i := 0; i < ipcSamplesPerCmd*3; i++ {
		agg.add(ipcCall{cmd: "x", elapsedMs: int64(i), ok: true, at: time.Unix(1, 0)})
	}
	if n := len(agg.cmds["x"].samples); n != ipcSamplesPerCmd {
		t.Fatalf("samples = %d", n)
	}
	if got := agg.summary().Commands[0]; got.Count != ipcSamplesPerCmd*3 || got.MaxMs != ipcSamplesPerCmd*3-1 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestProcessStatsLineShape(t *testing.T) {
	var sampler processSampler
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	fields := sampler.fields(&m)
	for _, key := range []string{"cpuUserSec", "cpuSystemSec", "maxRssBytes", "rssBytes", "openFds", "gcPauseTotalNs", "gcPauseDeltaNs", "numGcDelta"} {
		v, ok := fields[key]
		if !ok {
			t.Fatalf("missing %s", key)
		}
		raw, _ := json.Marshal(v)
		if strings.HasPrefix(string(raw), "-") {
			t.Fatalf("%s is negative: %s", key, raw)
		}
	}
	if again := sampler.fields(&m); again["gcPauseDeltaNs"].(uint64) != 0 || again["numGcDelta"].(uint32) != 0 {
		t.Fatalf("deltas must reset: %v", again)
	}
}

func TestCaptureWritesNewFiles(t *testing.T) {
	paths, logger := newEnv(t)
	opts := fastOptions()
	opts.StatsEvery = 10 * time.Millisecond
	s, err := Start(context.Background(), paths, logger, Info{Mode: "test"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	wails := logger.WailsLogger()
	const secret = "TOP-SECRET-VALUE-123"
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("call-%d", i)
		wails.Debug("Binding call started:", "id", id)
		time.Sleep(2 * time.Millisecond)
		wails.Debug("Binding call complete:", "id", id,
			"args", `{"cmd":"term.write","data":"`+secret+`"}`, "result", `{"ok":true,"data":"`+secret+`"}`)
	}
	RecordEvent("terminal.output", PayloadSize(secret))
	RecordEvent("stream:binary", PayloadSize([]byte(secret)))
	time.Sleep(60 * time.Millisecond)
	s.Stop()

	var sum ipcSummary
	data, err := os.ReadFile(filepath.Join(s.Dir(), IPCSummaryFile))
	if err != nil || json.Unmarshal(data, &sum) != nil {
		t.Fatalf("ipc summary: %v", err)
	}
	if len(sum.Commands) != 1 || sum.Commands[0].Cmd != "term.write" || sum.Commands[0].Count != 3 {
		t.Fatalf("summary = %+v", sum)
	}
	events := readJSONLines(t, filepath.Join(s.Dir(), EventStatsFile))
	if len(events) == 0 {
		t.Fatal("no event windows")
	}
	if rows := readJSONLines(t, filepath.Join(s.Dir(), "runtime-stats.jsonl")); len(rows) == 0 || rows[0]["cpuUserSec"] == nil {
		t.Fatal("runtime stats lack process fields")
	}
	meta, _ := os.ReadFile(filepath.Join(s.Dir(), "meta.txt"))
	if !strings.Contains(string(meta), EventStatsFile) || !strings.Contains(string(meta), IPCSummaryFile) {
		t.Fatal("meta.txt does not list the new files")
	}
	// Privacy: no written file may contain the secret.
	err = filepath.Walk(s.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, ".pprof") || strings.HasSuffix(path, ".out") {
			return err
		}
		content, _ := os.ReadFile(path)
		if bytes.Contains(content, []byte(secret)) {
			t.Errorf("secret leaked into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	RecordEvent("after-stop", 1) // recorders are detached after Stop
	if eventSink.Load() != nil {
		t.Fatal("event sink still attached")
	}
}
