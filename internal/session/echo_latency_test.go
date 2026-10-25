//go:build darwin || linux

package session

import (
	"context"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// echoStamps holds the monotonic-nanosecond marks of the keystroke in flight. Each field is written at most once per keystroke
// (compare-and-swap from zero) so the first event after the injection wins; the harness resets them between keystrokes.
type echoStamps struct {
	written atomic.Int64 // transport Write returned
	read    atomic.Int64 // pump's PTY read returned data
	fed     atomic.Int64 // emulator Feed returned
}

func (s *echoStamps) reset() { s.written.Store(0); s.read.Store(0); s.fed.Store(0) }

func stampOnce(v *atomic.Int64) { v.CompareAndSwap(0, time.Now().UnixNano()) }

type echoStampTransport struct {
	base.Transport
	stamps *echoStamps
}

func (t *echoStampTransport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	channel, err := t.Transport.(base.PTYTransport).OpenPTY(ctx, options)
	if channel != nil {
		channel = &echoStampChannel{Channel: channel, stamps: t.stamps}
	}
	return channel, err
}

type echoStampChannel struct {
	base.Channel
	stamps *echoStamps
}

func (c *echoStampChannel) Read(p []byte) (int, error) {
	n, err := c.Channel.Read(p)
	if n > 0 && c.stamps.written.Load() != 0 {
		stampOnce(&c.stamps.read)
	}
	return n, err
}

// echoStampTerminal wraps the production terminal so the emulator stage is measured on the real implementation.
type echoStampTerminal struct {
	*coreTerminal
	stamps *echoStamps
}

func (t *echoStampTerminal) Feed(data []byte) {
	t.coreTerminal.Feed(data)
	if t.stamps.read.Load() != 0 {
		stampOnce(&t.stamps.fed)
	}
}

func echoPercentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(p*float64(len(sorted))))]
}

func logEchoStage(t *testing.T, name string, values []time.Duration) {
	t.Helper()
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	t.Logf("%-22s n=%d p50=%v p95=%v p99=%v max=%v", name, len(values),
		echoPercentile(values, 0.50), echoPercentile(values, 0.95), echoPercentile(values, 0.99), echoPercentile(values, 1))
}

// TestEchoLatencyStages drives a real local PTY running cat and reports per-stage keystroke echo latency. It only logs; run with
// NEXTERM_ECHO_KEYS=<n> (default 30) and -v. NEXTERM_ECHO_WINDOW=<duration> replaces every armed window (what-if). NEXTERM_ECHO_NOISE=1 adds a concurrent allocation load to expose GC and scheduler effects.
func TestEchoLatencyStages(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement harness")
	}
	keys := 30
	if v, err := strconv.Atoi(os.Getenv("NEXTERM_ECHO_KEYS")); err == nil && v > 0 {
		keys = v
	}
	stamps := &echoStamps{}
	connector := ConnectorFunc(func(_ context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return &echoStampTransport{Transport: local.NewWithConfig(local.Config{Shell: "/bin/cat"}), stamps: stamps}, nil
	})
	manager := NewManager(Config{Connector: connector, Terminals: TerminalFactoryFunc(func(config TerminalConfig) (TerminalState, error) {
		state, err := terminalFactory{}.NewTerminal(config)
		if err != nil {
			return nil, err
		}
		return &echoStampTerminal{coreTerminal: state.(*coreTerminal), stamps: stamps}, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "echo-latency", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "a-1")
	receiver := bindTestReceiver(t, manager, "a-1")
	receiveTestAnchor(t, receiver)
	if os.Getenv("NEXTERM_ECHO_NOISE") != "" {
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			var sink [][]byte
			for {
				select {
				case <-stop:
					return
				default:
				}
				sink = append(sink, make([]byte, 64<<10))
				if len(sink) > 256 {
					sink = nil
				}
			}
		}()
	}
	rng := rand.New(rand.NewSource(1))
	var write, ptyRead, feed, flush, total []time.Duration
	// Record the window the coalescer chose for every armed timer and how late the runtime fired it.
	var armed, fired atomic.Int64
	var windowMu sync.Mutex
	var windows, lateness []time.Duration
	co := tab.subscribers["a-1"].co
	co.mu.Lock()
	co.afterFunc = func(d time.Duration, f func()) stopper {
		armed.Add(1)
		at := time.Now()
		if override, err := time.ParseDuration(os.Getenv("NEXTERM_ECHO_WINDOW")); err == nil {
			d = override // what-if experiment: replace the chosen window
		}
		return time.AfterFunc(d, func() {
			late := time.Since(at) - d
			windowMu.Lock()
			windows = append(windows, d)
			lateness = append(lateness, late)
			windowMu.Unlock()
			fired.Add(1)
			f()
		})
	}
	co.mu.Unlock()
	for i := 0; i < keys; i++ {
		time.Sleep(time.Duration(12+rng.Intn(40)) * time.Millisecond)
		stamps.reset()
		key := []byte{byte('a' + i%26)}
		start := time.Now()
		stamps.written.Store(0)
		if err := manager.Write(context.Background(), tab.ID, "client-a", key); err != nil {
			t.Fatal(err)
		}
		wrote := time.Now()
		stamps.written.Store(wrote.UnixNano())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var got time.Time
		for {
			frame, err := receiver.Next(ctx)
			if err != nil {
				cancel()
				t.Fatalf("key %d: %v", i, err)
			}
			_ = receiver.Ack(frame.Sequence)
			if len(frame.Data) > 0 {
				got = time.Now()
				break
			}
		}
		cancel()
		r, f := stamps.read.Load(), stamps.fed.Load()
		if r == 0 || f == 0 {
			continue // echo raced the stamp; the sample is not attributable
		}
		write = append(write, wrote.Sub(start))
		ptyRead = append(ptyRead, time.Duration(r-wrote.UnixNano()))
		feed = append(feed, time.Duration(f-r))
		flush = append(flush, got.Sub(time.Unix(0, f)))
		total = append(total, got.Sub(start))
	}
	logEchoStage(t, "write accepted", write)
	logEchoStage(t, "PTY echo read", ptyRead)
	logEchoStage(t, "emulator feed", feed)
	logEchoStage(t, "coalesce+send", flush)
	logEchoStage(t, "total", total)
	windowMu.Lock()
	logEchoStage(t, "armed window", windows)
	logEchoStage(t, "timer lateness", lateness)
	short := 0
	for _, w := range windows {
		if w == outputCoalesceIdleGrace {
			short++
		}
	}
	t.Logf("timers armed=%d fired=%d idle-grace=%d full-window=%d", armed.Load(), fired.Load(), short, len(windows)-short)
	windowMu.Unlock()
}
