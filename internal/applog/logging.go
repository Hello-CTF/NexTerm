package applog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

type Logger struct {
	*slog.Logger
	file  *os.File
	level *slog.LevelVar
	tee   *teeState
}

func NewLogger(paths platform.Paths, stderr io.Writer) (*Logger, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	writer := stderr
	var file *os.File
	err := os.MkdirAll(paths.LogDir, 0o700)
	if err == nil {
		file, err = os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			writer = io.MultiWriter(stderr, file)
		}
	}
	level := &slog.LevelVar{}
	level.Set(slog.LevelInfo)
	tee := &teeState{}
	primary := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})
	logger := &Logger{
		Logger: slog.New(NewRedactingHandler(newFanoutHandler(primary, tee))),
		file:   file,
		level:  level,
		tee:    tee,
	}
	return logger, err
}

// Level exposes the level of the primary sinks (console and log file); it defaults to Info.
func (l *Logger) Level() *slog.LevelVar {
	if l == nil {
		return nil
	}
	return l.level
}

// AttachDebugTee mirrors every record at Debug level and above to w, in addition to the regular sinks.
// Records pass through the redacting handler first, so w never sees unmasked secrets. A record enabled for
// the primary sinks is written to them once; the tee only adds a second copy. The returned function detaches
// the tee; attaching a new tee replaces the previous one.
func (l *Logger) AttachDebugTee(w io.Writer) (detach func()) {
	if l == nil || l.tee == nil || w == nil {
		return func() {}
	}
	cfg := &teeConfig{w: &lockedWriter{w: w}}
	l.tee.cfg.Store(cfg)
	return func() { l.tee.cfg.CompareAndSwap(cfg, nil) }
}

func NewRedactingHandler(next slog.Handler) slog.Handler {
	return redactingHandler{next: next}
}

type redactingHandler struct {
	next           slog.Handler
	sensitiveGroup bool
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	redacted := slog.NewRecord(record.Time, record.Level, RedactText(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		redacted.AddAttrs(redactLogAttr(attr, h.sensitiveGroup))
		return true
	})
	return h.next.Handle(ctx, redacted)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		redacted[i] = redactLogAttr(attr, h.sensitiveGroup)
	}
	return redactingHandler{next: h.next.WithAttrs(redacted), sensitiveGroup: h.sensitiveGroup}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(RedactText(name)), sensitiveGroup: h.sensitiveGroup || sensitiveLogKey(name)}
}

func redactLogAttr(attr slog.Attr, sensitiveGroup bool) slog.Attr {
	key := RedactText(attr.Key)
	if sensitiveGroup || sensitiveLogKey(attr.Key) {
		return slog.String(key, "***")
	}
	attr.Key = key
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindGroup:
		group := value.Group()
		children := make([]slog.Attr, len(group))
		for i, child := range group {
			children[i] = redactLogAttr(child, false)
		}
		attr.Value = slog.GroupValue(children...)
	case slog.KindString:
		attr.Value = slog.StringValue(RedactText(value.String()))
	case slog.KindAny:
		attr.Value = slog.StringValue(RedactText(fmt.Sprint(value.Any())))
	default:
		attr.Value = value
	}
	return attr
}

func sensitiveLogKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	for _, marker := range []string{"password", "passwd", "passphrase", "secret", "token", "apikey", "accesskey", "privatekey", "credential", "authorization", "cookie"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "pwd" || key == "auth" || strings.HasSuffix(key, "key")
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
