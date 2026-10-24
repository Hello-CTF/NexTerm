package applog

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IPCCall is the sanitized summary of one finished IPC call: identifiers, sizes and timing only.
type IPCCall struct {
	Cmd         string
	ElapsedMs   int64 // -1 when the start record was not seen
	OK          bool
	ArgsBytes   int
	ResultBytes int
	Time        time.Time
}

var ipcObserver atomic.Pointer[func(IPCCall)]

// SetIPCObserver installs (or, with nil, removes) a callback that receives every sanitized IPC call while a
// debug capture is attached. It is only reached on the capture path, so it costs nothing otherwise.
func SetIPCObserver(fn func(IPCCall)) {
	if fn == nil {
		ipcObserver.Store(nil)
		return
	}
	ipcObserver.Store(&fn)
}

// WailsLogger returns the logger handed to the Wails framework. Records at Info and above pass through. Its
// debug output carries the full arguments and results of every IPC call (including typed keystrokes), so it is
// never forwarded as is: while a debug capture is attached it is replaced by a sanitized timeline (command name,
// duration, outcome and payload sizes) and everything outside a short allow-list is dropped.
func (l *Logger) WailsLogger() *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	active := func() bool { return l != nil && l.tee != nil && l.tee.cfg.Load() != nil }
	return slog.New(&wailsHandler{next: l.Logger.Handler(), active: active, shared: &wailsShared{starts: make(map[string]time.Time)}})
}

type wailsShared struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

const maxPendingWailsCalls = 4096

type wailsHandler struct {
	next   slog.Handler
	active func() bool
	shared *wailsShared
}

func (h *wailsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level >= slog.LevelInfo {
		return h.next.Enabled(ctx, level)
	}
	return h.active() && h.next.Enabled(ctx, level)
}

func (h *wailsHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= slog.LevelInfo {
		return h.next.Handle(ctx, record)
	}
	switch record.Message {
	case "Binding call started:":
		h.rememberStart(record)
		return nil
	case "Binding call complete:":
		return h.next.Handle(ctx, h.sanitizeCall(record))
	case "Asset Request:":
		return h.handleAsset(ctx, record)
	case "Registering bound method:", "Starting up service:":
		return h.next.Handle(ctx, record)
	}
	return nil
}

func (h *wailsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &wailsHandler{next: h.next.WithAttrs(attrs), active: h.active, shared: h.shared}
}

func (h *wailsHandler) WithGroup(name string) slog.Handler {
	return &wailsHandler{next: h.next.WithGroup(name), active: h.active, shared: h.shared}
}

func recordAttr(record slog.Record, key string) (slog.Value, bool) {
	var value slog.Value
	found := false
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			value, found = attr.Value.Resolve(), true
			return false
		}
		return true
	})
	return value, found
}

func (h *wailsHandler) rememberStart(record slog.Record) {
	id, ok := recordAttr(record, "id")
	if !ok {
		return
	}
	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()
	if len(h.shared.starts) >= maxPendingWailsCalls {
		h.shared.starts = make(map[string]time.Time)
	}
	h.shared.starts[id.String()] = record.Time
}

func (h *wailsHandler) sanitizeCall(record slog.Record) slog.Record {
	var id, args, result string
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "id":
			id = attr.Value.Resolve().String()
		case "args":
			args = attr.Value.Resolve().String()
		case "result":
			result = attr.Value.Resolve().String()
		}
		return true
	})
	elapsed := int64(-1)
	h.shared.mu.Lock()
	if started, ok := h.shared.starts[id]; ok {
		elapsed = record.Time.Sub(started).Milliseconds()
		delete(h.shared.starts, id)
	}
	h.shared.mu.Unlock()

	out := slog.NewRecord(record.Time, slog.LevelDebug, "ipc call", record.PC)
	ok := strings.HasPrefix(result, `{"ok":true`)
	out.AddAttrs(
		slog.String("cmd", callCommand(args)),
		slog.Int64("elapsed_ms", elapsed),
		slog.Bool("ok", ok),
		slog.Int("args_bytes", len(args)),
		slog.Int("result_bytes", len(result)),
	)
	if !ok {
		out.AddAttrs(slog.String("err_code", quotedField(result, `"code":"`)))
	}
	if observe := ipcObserver.Load(); observe != nil {
		(*observe)(IPCCall{Cmd: quotedField(args, `"cmd":"`), ElapsedMs: elapsed, OK: ok, ArgsBytes: len(args), ResultBytes: len(result), Time: record.Time})
	}
	return out
}

// callCommand returns the command name of a call whose arguments look like `[{"cmd":"name",...`. Anything else
// is "unknown", so a nested field named cmd can never be picked up.
func callCommand(args string) string {
	if !strings.HasPrefix(args, `[{"cmd":"`) {
		return "unknown"
	}
	return quotedField(args, `"cmd":"`)
}

// quotedField reads the short identifier that follows marker, or "unknown". Only identifier characters are
// accepted, so no argument content can leak through this path.
func quotedField(text, marker string) string {
	start := strings.Index(text, marker)
	if start < 0 {
		return "unknown"
	}
	rest := text[start+len(marker):]
	end := 0
	for end < len(rest) && end < 64 {
		c := rest[end]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			end++
			continue
		}
		break
	}
	if end == 0 || end >= len(rest) || rest[end] != '"' {
		return "unknown"
	}
	return rest[:end]
}

// handleAsset keeps only slow or failed asset-server requests; the rest is noise at about three lines per call.
func (h *wailsHandler) handleAsset(ctx context.Context, record slog.Record) error {
	keep := false
	if code, ok := recordAttr(record, "code"); ok && code.Kind() == slog.KindInt64 && code.Int64() >= 400 {
		keep = true
	}
	if duration, ok := recordAttr(record, "duration"); ok && duration.Kind() == slog.KindDuration && duration.Duration() >= 50*time.Millisecond {
		keep = true
	}
	if !keep {
		return nil
	}
	return h.next.Handle(ctx, record)
}
