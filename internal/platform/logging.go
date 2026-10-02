package platform

import (
	"io"
	"log/slog"
	"os"
)

type Logger struct {
	*slog.Logger
	file *os.File
}

func NewLogger(paths Paths, stderr io.Writer) (*Logger, error) {
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
	logger := &Logger{
		Logger: slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})),
		file:   file,
	}
	return logger, err
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
