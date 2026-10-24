package diagcapture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/platform"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

// Options tunes the capture schedule; tests inject short durations.
type Options struct {
	CPUFirst         time.Duration // length of the first CPU profile
	CPULaterAt       time.Duration // offset of the second CPU profile
	CPULater         time.Duration // length of the second CPU profile
	TraceFor         time.Duration // length of the execution trace
	GoroutineFast    time.Duration // goroutine snapshot interval during the fast window
	GoroutineFastFor time.Duration // length of the fast window
	GoroutineSlow    time.Duration // goroutine snapshot interval afterwards
	ProfileEvery     time.Duration // heap, allocs, mutex and block profiles
	StatsEvery       time.Duration // runtime-stats.jsonl cadence
	MaxDuration      time.Duration // hard stop
	MaxBytes         int64         // soft folder cap; writes beyond it are dropped
	MutexFraction    int
	BlockRate        time.Duration
	Now              func() time.Time
}

func DefaultOptions() Options {
	return Options{
		CPUFirst: 60 * time.Second, CPULaterAt: 5 * time.Minute, CPULater: 30 * time.Second,
		TraceFor:      20 * time.Second,
		GoroutineFast: 10 * time.Second, GoroutineFastFor: 2 * time.Minute, GoroutineSlow: time.Minute,
		ProfileEvery: time.Minute, StatsEvery: 5 * time.Second,
		MaxDuration: 30 * time.Minute, MaxBytes: 200 << 20,
		MutexFraction: 5, BlockRate: time.Millisecond,
	}
}

// Info describes the launch for meta.txt.
type Info struct {
	Mode   string   // desktop or server
	Source string   // marker, flag or env
	Args   []string // command-line arguments; environment is never recorded
}

// Session is a running capture. It is process-global (profilers are), so only one can be active.
type Session struct {
	dir    string
	opts   Options
	logger *slog.Logger
	start  time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	bytes    atomic.Int64
	capped   atomic.Bool
	stopOnce sync.Once

	prevMutex int
	detachTee func()
	logFile   *os.File
	statsFile *os.File
}

var (
	activeMu sync.Mutex
	active   *Session
)

// Start consumes the arm marker first, so that a crash or an error below can never leave it sticky, then
// begins recording. A nil logger falls back to slog.Default.
func Start(ctx context.Context, paths platform.Paths, logger *applog.Logger, info Info, opts ...Options) (*Session, error) {
	markerErr := Disarm(paths)
	o := DefaultOptions()
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if markerErr != nil {
		return nil, markerErr
	}
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != nil {
		return nil, errors.New("a debug capture is already running")
	}
	root := Root(paths)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create capture folder: %w", err)
	}
	startTime := o.Now().UTC()
	dir, err := makeUniqueDir(root, startTime.Format("20060102T150405Z"))
	if err != nil {
		return nil, fmt.Errorf("create capture folder: %w", err)
	}
	slogger := slog.Default()
	if logger != nil && logger.Logger != nil {
		slogger = logger.Logger
	}
	s := &Session{dir: dir, opts: o, logger: slogger, start: time.Now(), done: make(chan struct{})}
	if err := s.writeMeta(startTime, info); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	if s.logFile, err = s.create("app.debug.log"); err != nil {
		return nil, err
	}
	if s.statsFile, err = s.create("runtime-stats.jsonl"); err != nil {
		_ = s.logFile.Close()
		return nil, err
	}
	if logger != nil {
		s.detachTee = logger.AttachDebugTee(&capWriter{s: s, w: s.logFile})
	}
	if o.MutexFraction > 0 {
		s.prevMutex = runtime.SetMutexProfileFraction(o.MutexFraction)
	} else {
		s.prevMutex = -1
	}
	if o.BlockRate > 0 {
		runtime.SetBlockProfileRate(int(o.BlockRate.Nanoseconds()))
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	active = s
	s.spawn(runCtx)
	return s, nil
}

func makeUniqueDir(root, name string) (string, error) {
	for i := 0; i < 100; i++ {
		candidate := filepath.Join(root, name)
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", candidate, i)
		}
		err := os.Mkdir(candidate, 0o700)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("no free capture folder name")
}

// Dir is the capture folder.
func (s *Session) Dir() string { return s.dir }

// Done is closed once the capture has fully stopped (explicitly, by timeout, or by context cancellation).
func (s *Session) Done() <-chan struct{} { return s.done }

// Capped reports whether the size cap stopped further writes.
func (s *Session) Capped() bool { return s.capped.Load() }

// Active returns the running capture, if any.
func Active() *Session {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

func (s *Session) create(name string) (*os.File, error) {
	return os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
}

func (s *Session) warn(msg string, err error) {
	s.logger.Warn("debug capture: "+msg, "error", err)
}

func (s *Session) writeMeta(start time.Time, info Info) error {
	var b strings.Builder
	fmt.Fprintf(&b, "app_version: %s\n", version.Version)
	fmt.Fprintf(&b, "mode: %s\n", info.Mode)
	fmt.Fprintf(&b, "os_arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "go_version: %s\n", runtime.Version())
	fmt.Fprintf(&b, "gomaxprocs: %d\n", runtime.GOMAXPROCS(0))
	fmt.Fprintf(&b, "num_cpu: %d\n", runtime.NumCPU())
	fmt.Fprintf(&b, "start_utc: %s\n", start.Format(time.RFC3339))
	fmt.Fprintf(&b, "trigger: %s\n", info.Source)
	fmt.Fprintf(&b, "args: %s\n", strings.Join(sanitizeArgs(info.Args), " "))
	o := s.opts
	fmt.Fprintf(&b, "settings: cpu_first=%s cpu_later_at=%s cpu_later=%s trace=%s goroutine_fast=%s/%s goroutine_slow=%s profile_every=%s stats_every=%s max_duration=%s max_bytes=%d mutex_fraction=%d block_rate=%s\n",
		o.CPUFirst, o.CPULaterAt, o.CPULater, o.TraceFor, o.GoroutineFast, o.GoroutineFastFor, o.GoroutineSlow, o.ProfileEvery, o.StatsEvery, o.MaxDuration, o.MaxBytes, o.MutexFraction, o.BlockRate)
	return os.WriteFile(filepath.Join(s.dir, "meta.txt"), []byte(b.String()), 0o600)
}

// sanitizeArgs masks connection strings and anything the log redactor recognises; the environment is never read.
func sanitizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	maskNext := false
	for _, arg := range args {
		switch {
		case maskNext:
			arg = "***"
			maskNext = false
		case arg == "--db-dsn":
			maskNext = true
		case strings.HasPrefix(arg, "--db-dsn="):
			arg = "--db-dsn=***"
		}
		out = append(out, applog.RedactText(arg))
	}
	return out
}

// capWriter drops writes once the folder cap is reached instead of failing the writer's owner.
type capWriter struct {
	s *Session
	w io.Writer
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.s.opts.MaxBytes > 0 && c.s.bytes.Load() >= c.s.opts.MaxBytes {
		c.s.capped.Store(true)
		return len(p), nil
	}
	n, err := c.w.Write(p)
	c.s.bytes.Add(int64(n))
	return n, err
}

func (s *Session) overCap() bool {
	if s.opts.MaxBytes > 0 && s.bytes.Load() >= s.opts.MaxBytes {
		s.capped.Store(true)
		return true
	}
	return false
}

func (s *Session) elapsed() time.Duration { return time.Since(s.start) }

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Session) spawn(ctx context.Context) {
	run := func(name string, fn func(context.Context)) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.logger.Warn("debug capture: worker panicked", "worker", name, "panic", fmt.Sprint(r))
				}
			}()
			fn(ctx)
		}()
	}
	run("cpu", s.runCPU)
	run("trace", s.runTrace)
	run("goroutines", s.runGoroutines)
	run("profiles", s.runProfiles)
	run("stats", s.runStats)
	go func() {
		if sleep(ctx, s.opts.MaxDuration) {
			s.finish("max duration reached")
			return
		}
		// Parent context cancelled (app shutdown) or Stop called.
		s.finish("stopped")
	}()
}

func (s *Session) runCPU(ctx context.Context) {
	if s.opts.CPUFirst > 0 {
		s.cpuProfile(ctx, "cpu-first.pprof", s.opts.CPUFirst)
	}
	if s.opts.CPULater <= 0 {
		return
	}
	if !sleep(ctx, s.opts.CPULaterAt-s.elapsed()) {
		return
	}
	s.cpuProfile(ctx, "cpu-later.pprof", s.opts.CPULater)
}

func (s *Session) cpuProfile(ctx context.Context, name string, d time.Duration) {
	if s.overCap() {
		return
	}
	f, err := s.create(name)
	if err != nil {
		s.warn("create "+name, err)
		return
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(&capWriter{s: s, w: f}); err != nil {
		s.warn("start cpu profile (another profiler may be running)", err)
		return
	}
	sleep(ctx, d)
	pprof.StopCPUProfile()
}

func (s *Session) runTrace(ctx context.Context) {
	if s.opts.TraceFor <= 0 || s.overCap() {
		return
	}
	f, err := s.create("trace-first-20s.out")
	if err != nil {
		s.warn("create trace", err)
		return
	}
	defer f.Close()
	if err := trace.Start(&capWriter{s: s, w: f}); err != nil {
		s.warn("start trace (another trace may be running)", err)
		return
	}
	sleep(ctx, s.opts.TraceFor)
	trace.Stop()
}

func (s *Session) writeProfile(sub, name, profile string, debug int) {
	if s.overCap() {
		return
	}
	p := pprof.Lookup(profile)
	if p == nil {
		return
	}
	dir := filepath.Join(s.dir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.warn("create "+sub, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		s.warn("create "+name, err)
		return
	}
	defer f.Close()
	if err := p.WriteTo(&capWriter{s: s, w: f}, debug); err != nil {
		s.warn("write "+name, err)
	}
}

func stamp(d time.Duration) string { return fmt.Sprintf("%06ds", int64(d.Seconds())) }

func (s *Session) runGoroutines(ctx context.Context) {
	for n := 0; ; n++ {
		s.writeProfile("goroutines", fmt.Sprintf("goroutine-%04d-%s.txt", n, stamp(s.elapsed())), "goroutine", 2)
		interval := s.opts.GoroutineSlow
		if s.elapsed() < s.opts.GoroutineFastFor {
			interval = s.opts.GoroutineFast
		}
		if interval <= 0 || !sleep(ctx, interval) {
			return
		}
	}
}

func (s *Session) runProfiles(ctx context.Context) {
	if s.opts.ProfileEvery <= 0 {
		return
	}
	for n := 0; sleep(ctx, s.opts.ProfileEvery); n++ {
		s.snapshotProfiles(n)
	}
}

func (s *Session) snapshotProfiles(n int) {
	suffix := fmt.Sprintf("%04d-%s.pprof", n, stamp(s.elapsed()))
	for _, name := range []string{"heap", "allocs", "mutex", "block"} {
		s.writeProfile("profiles", name+"-"+suffix, name, 0)
	}
}

func (s *Session) runStats(ctx context.Context) {
	if s.opts.StatsEvery <= 0 {
		return
	}
	w := &capWriter{s: s, w: s.statsFile}
	enc := json.NewEncoder(w)
	for {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		_ = enc.Encode(map[string]any{
			"t":          time.Now().UTC().Format(time.RFC3339Nano),
			"elapsedSec": int64(s.elapsed().Seconds()),
			"goroutines": runtime.NumGoroutine(),
			"heapAlloc":  m.HeapAlloc,
			"gcCount":    m.NumGC,
			"threads":    threadCount(), // OS threads created so far
		})
		if !sleep(ctx, s.opts.StatsEvery) {
			return
		}
	}
}

func threadCount() int {
	if p := pprof.Lookup("threadcreate"); p != nil {
		return p.Count()
	}
	return 0
}

// Stop ends the capture, waits for workers, restores process-wide profiler settings and writes the status file.
func (s *Session) Stop() {
	s.finish("stopped")
	<-s.done
}

func (s *Session) finish(reason string) {
	s.stopOnce.Do(func() {
		defer close(s.done)
		s.cancel()
		s.wg.Wait()
		if s.detachTee != nil {
			s.detachTee()
		}
		// A last snapshot shows the state at shutdown, which is what a hang report needs.
		s.writeProfile("goroutines", "goroutine-final.txt", "goroutine", 2)
		s.snapshotProfiles(9999)
		if s.prevMutex >= 0 {
			runtime.SetMutexProfileFraction(s.prevMutex)
		}
		if s.opts.BlockRate > 0 {
			runtime.SetBlockProfileRate(0)
		}
		status := fmt.Sprintf("end_utc: %s\nreason: %s\nbytes_written: %d\nsize_cap_bytes: %d\nsize_cap_reached: %t\n",
			time.Now().UTC().Format(time.RFC3339), reason, s.bytes.Load(), s.opts.MaxBytes, s.capped.Load())
		if s.capped.Load() {
			status += "note: size cap reached; later log lines and snapshots were not recorded\n"
		}
		if err := os.WriteFile(filepath.Join(s.dir, "capture-status.txt"), []byte(status), 0o600); err != nil {
			s.warn("write capture-status.txt", err)
		}
		_ = s.logFile.Close()
		_ = s.statsFile.Close()
		activeMu.Lock()
		if active == s {
			active = nil
		}
		activeMu.Unlock()
	})
}

// StartIfRequested starts a capture when the arm marker exists or force is set (flag or environment). It never
// fails the caller: errors are logged and nil is returned.
func StartIfRequested(ctx context.Context, paths platform.Paths, logger *applog.Logger, info Info, force bool) *Session {
	armed := IsArmed(paths)
	if !armed && !force {
		return nil
	}
	if armed {
		info.Source = "arm-marker"
	} else {
		info.Source = "flag-or-env"
	}
	session, err := Start(ctx, paths, logger, info)
	if err != nil {
		slog.Warn("debug capture could not start", "error", err)
		return nil
	}
	session.logger.Info("debug capture is active for this run only; profiles and debug logs are being saved", "dir", session.Dir())
	return session
}
