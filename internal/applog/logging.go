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
	file     *rotatingFile
	filePath string
	level    *slog.LevelVar
	tee      *teeState
}

// NewLogger writes text records at Info level and above to stderr and to the rotating log file.
func NewLogger(paths platform.Paths, stderr io.Writer) (*Logger, error) {
	return NewLoggerWithOptions(paths, stderr, DefaultOptions())
}

// NewLoggerWithOptions builds the logger described by options. When the log file cannot be opened the logger
// still writes to stderr and the error is returned; a file sink skipped on purpose is not an error.
func NewLoggerWithOptions(paths platform.Paths, stderr io.Writer, options Options) (*Logger, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	writer := stderr
	var file *rotatingFile
	var err error
	if fileSinkWanted(options, stderr) {
		err = os.MkdirAll(paths.LogDir, 0o700)
		if err == nil {
			maxBackups := options.MaxBackups
			if maxBackups == 0 && options.MaxFileBytes == 0 {
				maxBackups = DefaultMaxBackups
			}
			file, err = openRotatingFile(paths.LogFile, options.MaxFileBytes, maxBackups)
			if err == nil {
				writer = io.MultiWriter(stderr, file)
			}
		}
	}
	level := &slog.LevelVar{}
	level.Set(options.Level)
	tee := &teeState{}
	handlerOptions := &slog.HandlerOptions{Level: level}
	var primary slog.Handler
	if options.Format == FormatJSON {
		primary = slog.NewJSONHandler(writer, handlerOptions)
	} else {
		primary = slog.NewTextHandler(writer, handlerOptions)
	}
	logger := &Logger{
		Logger: slog.New(NewRedactingHandler(newFanoutHandler(primary, tee))),
		file:   file,
		level:  level,
		tee:    tee,
	}
	if file != nil {
		logger.filePath = paths.LogFile
	}
	return logger, err
}

func fileSinkWanted(options Options, stderr io.Writer) bool {
	switch options.File {
	case FileOff:
		return false
	case FileOn:
		return true
	}
	if !options.SkipFileUnderJournal || options.JournalStream == "" {
		return true
	}
	f, ok := stderr.(*os.File)
	return !ok || !stderrIsJournal(options.JournalStream, f)
}

// FilePath returns the log file being written, or "" when records only go to stderr.
func (l *Logger) FilePath() string {
	if l == nil {
		return ""
	}
	return l.filePath
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
