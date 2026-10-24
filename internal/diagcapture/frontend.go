package diagcapture

import (
	"errors"
	"os"
	"path/filepath"
)

// FrontendFile is the per-capture file that receives the web view's performance windows, one JSON line each.
const FrontendFile = "frontend-perf.jsonl"

// maxFrontendBytes bounds the frontend file independently of the capture size cap.
const maxFrontendBytes = 5 << 20

// ErrNoActiveCapture is returned when frontend data arrives while no capture is running.
var ErrNoActiveCapture = errors.New("no debug capture is running")

// ErrFrontendFull is returned once the frontend file reached its size cap.
var ErrFrontendFull = errors.New("frontend performance file is full")

// AppendFrontend appends one already-validated JSON line to the running capture's frontend file.
func AppendFrontend(line []byte) error {
	session := Active()
	if session == nil {
		return ErrNoActiveCapture
	}
	return session.appendFrontend(line)
}

func (s *Session) appendFrontend(line []byte) error {
	s.feMu.Lock()
	defer s.feMu.Unlock()
	if s.feSize+int64(len(line))+1 > maxFrontendBytes {
		return ErrFrontendFull
	}
	file, err := os.OpenFile(filepath.Join(s.dir, FrontendFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := file.Write(append(append([]byte(nil), line...), '\n'))
	s.feSize += int64(n)
	return err
}
