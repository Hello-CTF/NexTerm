package applog

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// FileMode selects whether <data-dir>/logs/nexterm.log is written.
type FileMode string

const (
	// FileAuto writes the file unless Options.SkipFileUnderJournal is set and stderr is the systemd journal.
	FileAuto FileMode = "auto"
	FileOn   FileMode = "on"
	FileOff  FileMode = "off"
)

const (
	EnvLogLevel  = "NEXTERM_LOG_LEVEL"
	EnvLogFormat = "NEXTERM_LOG_FORMAT"
	EnvLogFile   = "NEXTERM_LOG_FILE"
)

type Options struct {
	Level  slog.Level
	Format Format
	File   FileMode
	// SkipFileUnderJournal lets FileAuto drop the file sink when stderr already reaches journald.
	SkipFileUnderJournal bool
	// JournalStream is the JOURNAL_STREAM value that systemd set for this process.
	JournalStream string
	// MaxFileBytes and MaxBackups configure rotation; zero values use the defaults.
	MaxFileBytes int64
	MaxBackups   int
}

// DefaultOptions keeps the historical behavior: Info level, text format, file sink on.
func DefaultOptions() Options {
	return Options{Level: slog.LevelInfo, Format: FormatText, File: FileAuto, MaxBackups: DefaultMaxBackups}
}

// ParseEnv reads NEXTERM_LOG_LEVEL, NEXTERM_LOG_FORMAT, NEXTERM_LOG_FILE and JOURNAL_STREAM. An invalid value
// keeps that field's default and is reported in the returned error, so callers can choose to fail or warn.
func ParseEnv(getenv func(string) string) (Options, error) {
	options := DefaultOptions()
	if getenv == nil {
		return options, nil
	}
	options.JournalStream = getenv("JOURNAL_STREAM")
	var errs []error
	if raw := strings.TrimSpace(getenv(EnvLogLevel)); raw != "" {
		level, err := ParseLevel(raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			options.Level = level
		}
	}
	if raw := strings.TrimSpace(getenv(EnvLogFormat)); raw != "" {
		switch Format(strings.ToLower(raw)) {
		case FormatText:
			options.Format = FormatText
		case FormatJSON:
			options.Format = FormatJSON
		default:
			errs = append(errs, fmt.Errorf("invalid %s value %q (want text or json)", EnvLogFormat, raw))
		}
	}
	if raw := strings.TrimSpace(getenv(EnvLogFile)); raw != "" {
		switch FileMode(strings.ToLower(raw)) {
		case FileAuto:
			options.File = FileAuto
		case FileOn:
			options.File = FileOn
		case FileOff:
			options.File = FileOff
		default:
			errs = append(errs, fmt.Errorf("invalid %s value %q (want auto, on or off)", EnvLogFile, raw))
		}
	}
	return options, errors.Join(errs...)
}

func ParseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid %s value %q (want debug, info, warn or error)", EnvLogLevel, raw)
	}
}
