package applog

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

type teeConfig struct{ w io.Writer }

type teeState struct{ cfg atomic.Pointer[teeConfig] }

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type handlerOp struct {
	attrs []slog.Attr
	group string
}

// fanoutHandler writes to the primary handler according to its own level and, while a tee is attached, also
// to a Debug-level handler. Derived loggers created before the tee was attached still reach it because the
// tee handler is rebuilt lazily by replaying the recorded With/WithGroup operations.
type fanoutHandler struct {
	primary slog.Handler
	state   *teeState
	ops     []handlerOp

	mu       sync.Mutex
	cachedBy *teeConfig
	cached   slog.Handler
}

func newFanoutHandler(primary slog.Handler, state *teeState) *fanoutHandler {
	return &fanoutHandler{primary: primary, state: state}
}

func (h *fanoutHandler) tee() slog.Handler {
	cfg := h.state.cfg.Load()
	if cfg == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cachedBy == cfg {
		return h.cached
	}
	var next slog.Handler = slog.NewTextHandler(cfg.w, &slog.HandlerOptions{Level: slog.LevelDebug})
	for _, op := range h.ops {
		if op.group != "" {
			next = next.WithGroup(op.group)
		} else {
			next = next.WithAttrs(op.attrs)
		}
	}
	h.cachedBy, h.cached = cfg, next
	return next
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.primary.Enabled(ctx, level) {
		return true
	}
	return level >= slog.LevelDebug && h.state.cfg.Load() != nil
}

func (h *fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	if h.primary.Enabled(ctx, record.Level) {
		err = h.primary.Handle(ctx, record.Clone())
	}
	if tee := h.tee(); tee != nil && tee.Enabled(ctx, record.Level) {
		if teeErr := tee.Handle(ctx, record); err == nil {
			err = teeErr
		}
	}
	return err
}

func (h *fanoutHandler) derive(primary slog.Handler, op handlerOp) *fanoutHandler {
	ops := make([]handlerOp, len(h.ops), len(h.ops)+1)
	copy(ops, h.ops)
	return &fanoutHandler{primary: primary, state: h.state, ops: append(ops, op)}
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.derive(h.primary.WithAttrs(attrs), handlerOp{attrs: attrs})
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.derive(h.primary.WithGroup(name), handlerOp{group: name})
}
