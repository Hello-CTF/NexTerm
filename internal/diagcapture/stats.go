package diagcapture

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Everything below records names, counts, bytes and durations only: never arguments, results or payload
// content. Recorders are active only while a capture runs and cost one atomic load otherwise.

const (
	// EventStatsFile receives one JSON line per window with per-topic event counts and bytes.
	EventStatsFile     = "event-stats.jsonl"
	IPCSummaryFile     = "ipc-summary.json"
	maxEventStatsBytes = 5 << 20
	maxTopicsPerWindow = 64
	maxTopicLen        = 96
	maxIPCCommands     = 256
	ipcSamplesPerCmd   = 1024
	ipcSlowestKept     = 20
	otherName          = "_other"
)

type processUsage struct {
	UserSec     float64
	SystemSec   float64
	MaxRSSBytes int64
}

// processSampler turns cumulative runtime counters into per-interval values for runtime-stats.jsonl.
type processSampler struct {
	prevNumGC   uint32
	prevPauseNs uint64
}

func (p *processSampler) fields(m *runtime.MemStats) map[string]any {
	usage := readProcessUsage()
	out := map[string]any{
		"cpuUserSec":     usage.UserSec,
		"cpuSystemSec":   usage.SystemSec,
		"maxRssBytes":    usage.MaxRSSBytes,
		"rssBytes":       currentRSSBytes(),
		"openFds":        openFDCount(),
		"gcPauseTotalNs": m.PauseTotalNs,
		"gcPauseDeltaNs": m.PauseTotalNs - p.prevPauseNs,
		"numGcDelta":     m.NumGC - p.prevNumGC,
	}
	p.prevNumGC, p.prevPauseNs = m.NumGC, m.PauseTotalNs
	return out
}

// ---- event stats ----

type eventCounter struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

type eventAggregator struct {
	mu       sync.Mutex
	topics   map[string]*eventCounter
	overflow eventCounter
	dropped  int // distinct topics folded into overflow
	file     *os.File
	written  int64
	now      func() time.Time
	window   time.Time
}

var eventSink atomic.Pointer[eventAggregator]

// RecordEvent counts one Go-to-frontend emission. payloadBytes is best effort (0 when unknown).
func RecordEvent(topic string, payloadBytes int) {
	if a := eventSink.Load(); a != nil {
		a.record(topic, payloadBytes)
	}
}

// PayloadSize is the best-effort byte size of an event payload without serialising it.
func PayloadSize(payload any) int {
	switch v := payload.(type) {
	case []byte:
		return len(v)
	case string:
		return len(v)
	case json.RawMessage:
		return len(v)
	}
	return 0
}

func newEventAggregator(file *os.File, now func() time.Time) *eventAggregator {
	return &eventAggregator{topics: make(map[string]*eventCounter), file: file, now: now, window: now()}
}

func (a *eventAggregator) record(topic string, payloadBytes int) {
	if payloadBytes < 0 {
		payloadBytes = 0
	}
	if len(topic) > maxTopicLen {
		topic = topic[:maxTopicLen]
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.topics[topic]
	if c == nil {
		if len(a.topics) >= maxTopicsPerWindow {
			a.overflow.Count++
			a.overflow.Bytes += int64(payloadBytes)
			a.dropped++
			return
		}
		c = &eventCounter{}
		a.topics[topic] = c
	}
	c.Count++
	c.Bytes += int64(payloadBytes)
}

type eventWindowLine struct {
	T         string                   `json:"t"`
	WindowSec float64                  `json:"windowSec"`
	Topics    map[string]*eventCounter `json:"topics"`
	Other     *eventCounter            `json:"other,omitempty"`
}

// flush writes the current window as one line and starts a new one; empty windows are skipped.
func (a *eventAggregator) flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	line := eventWindowLine{T: now.UTC().Format(time.RFC3339Nano), WindowSec: now.Sub(a.window).Seconds(), Topics: a.topics}
	empty := len(a.topics) == 0 && a.overflow.Count == 0
	if a.overflow.Count > 0 {
		o := a.overflow
		line.Other = &o
	}
	a.topics, a.overflow, a.dropped, a.window = make(map[string]*eventCounter), eventCounter{}, 0, now
	if empty || a.file == nil {
		return
	}
	data, err := json.Marshal(line)
	if err != nil || a.written+int64(len(data))+1 > maxEventStatsBytes {
		return
	}
	n, _ := a.file.Write(append(data, '\n'))
	a.written += int64(n)
}

func (s *Session) runEventStats(ctx context.Context, agg *eventAggregator) {
	if s.opts.StatsEvery <= 0 {
		return
	}
	t := time.NewTicker(s.opts.StatsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			agg.flush()
		}
	}
}

// ---- IPC summary ----

type ipcCmdStats struct {
	count, errors, totalMs, maxMs, argsBytes, resultBytes int64
	timed                                                 int64 // calls with a known duration
	samples                                               []int64
}

type slowCall struct {
	Cmd         string `json:"cmd"`
	ElapsedMs   int64  `json:"elapsedMs"`
	ResultBytes int    `json:"resultBytes"`
	Time        string `json:"time"`
}

type ipcAggregator struct {
	mu      sync.Mutex
	cmds    map[string]*ipcCmdStats
	slowest []slowCall
	rng     *rand.Rand
}

func newIPCAggregator() *ipcAggregator {
	return &ipcAggregator{cmds: make(map[string]*ipcCmdStats), rng: rand.New(rand.NewSource(1))}
}

type ipcCall struct {
	cmd                  string
	elapsedMs            int64
	ok                   bool
	argsBytes, resultLen int
	at                   time.Time
}

func (a *ipcAggregator) add(c ipcCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := c.cmd
	st := a.cmds[name]
	if st == nil {
		if len(a.cmds) >= maxIPCCommands {
			name = otherName
			st = a.cmds[name]
		}
		if st == nil {
			st = &ipcCmdStats{}
			a.cmds[name] = st
		}
	}
	st.count++
	if !c.ok {
		st.errors++
	}
	st.argsBytes += int64(c.argsBytes)
	st.resultBytes += int64(c.resultLen)
	if c.elapsedMs < 0 {
		return
	}
	st.timed++
	st.totalMs += c.elapsedMs
	if c.elapsedMs > st.maxMs {
		st.maxMs = c.elapsedMs
	}
	// Reservoir sampling keeps percentile memory bounded.
	if len(st.samples) < ipcSamplesPerCmd {
		st.samples = append(st.samples, c.elapsedMs)
	} else if j := a.rng.Int63n(st.timed); j < ipcSamplesPerCmd {
		st.samples[j] = c.elapsedMs
	}
	a.noteSlow(slowCall{Cmd: name, ElapsedMs: c.elapsedMs, ResultBytes: c.resultLen, Time: c.at.UTC().Format(time.RFC3339Nano)})
}

func (a *ipcAggregator) noteSlow(call slowCall) {
	if len(a.slowest) >= ipcSlowestKept && call.ElapsedMs <= a.slowest[len(a.slowest)-1].ElapsedMs {
		return
	}
	i := sort.Search(len(a.slowest), func(i int) bool { return a.slowest[i].ElapsedMs < call.ElapsedMs })
	a.slowest = append(a.slowest, slowCall{})
	copy(a.slowest[i+1:], a.slowest[i:])
	a.slowest[i] = call
	if len(a.slowest) > ipcSlowestKept {
		a.slowest = a.slowest[:ipcSlowestKept]
	}
}

type ipcCmdSummary struct {
	Cmd         string `json:"cmd"`
	Count       int64  `json:"count"`
	Errors      int64  `json:"errors"`
	TotalMs     int64  `json:"totalMs"`
	P50Ms       int64  `json:"p50Ms"`
	P95Ms       int64  `json:"p95Ms"`
	MaxMs       int64  `json:"maxMs"`
	ArgsBytes   int64  `json:"argsBytes"`
	ResultBytes int64  `json:"resultBytes"`
}

type ipcSummary struct {
	Commands []ipcCmdSummary `json:"commands"`
	Slowest  []slowCall      `json:"slowest"`
}

func (a *ipcAggregator) summary() ipcSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := ipcSummary{Commands: make([]ipcCmdSummary, 0, len(a.cmds)), Slowest: append([]slowCall{}, a.slowest...)}
	for name, st := range a.cmds {
		sorted := append([]int64(nil), st.samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		out.Commands = append(out.Commands, ipcCmdSummary{
			Cmd: name, Count: st.count, Errors: st.errors, TotalMs: st.totalMs,
			P50Ms: percentile(sorted, 50), P95Ms: percentile(sorted, 95), MaxMs: st.maxMs,
			ArgsBytes: st.argsBytes, ResultBytes: st.resultBytes,
		})
	}
	sort.Slice(out.Commands, func(i, j int) bool {
		if out.Commands[i].TotalMs != out.Commands[j].TotalMs {
			return out.Commands[i].TotalMs > out.Commands[j].TotalMs
		}
		return out.Commands[i].Cmd < out.Commands[j].Cmd
	})
	return out
}

// percentile is the nearest-rank percentile of an ascending slice.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func (a *ipcAggregator) write(dir string) error {
	data, err := json.MarshalIndent(a.summary(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, IPCSummaryFile), data, 0o600)
}
